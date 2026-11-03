"""Local settings and real Go membership CLI tests, using isolated databases."""
from collections import deque
from contextlib import closing
import hashlib
import http.client
import json
from pathlib import Path
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import patch
from uuid import uuid4
from urllib.parse import urlencode
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
import local_server as server
from test_local_server import Client
from rf4_local_options import (DEBUG_STATE_KEY, DEBUG_VALUES, LocalOptionsError,
                               debug_status, ensure_debug_defaults, membership, set_debug)


class LocalOptionsTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.reference = ROOT.parent
        cls.binary = cls.reference / 'bin' / 'local-membership.exe'
        if not cls.binary.is_file():
            raise unittest.SkipTest('Build the local membership service first')
        cls.template_directory = tempfile.TemporaryDirectory(prefix='rf4-options-template-')
        cls.template = Path(cls.template_directory.name) / 'gameplay.sqlite3'
        cls.template.touch()
        result = subprocess.run([str(cls.binary), '--database', str(cls.template), '--uid', '7357', 'status'],
                                capture_output=True, encoding='utf-8', timeout=20,
                                creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
        if json.loads(result.stdout).get('error', {}).get('code') != 'user_not_found':
            raise AssertionError('Go did not initialize the isolated database')
        with closing(sqlite3.connect(cls.template)) as db, db:
            db.executemany('INSERT INTO profile(uid,name,gold) VALUES(?,?,?)',
                           [(7357, 'local-owner', 10000), (2, 'other-local', 60000)])

    @classmethod
    def tearDownClass(cls):
        cls.template_directory.cleanup()

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='rf4-local-options-')
        self.directory = Path(self.temporary.name)
        self.database = self.directory / 'gameplay.sqlite3'
        with closing(sqlite3.connect(self.template)) as original:
            with closing(sqlite3.connect(self.database)) as copied:
                original.backup(copied)

    def tearDown(self):
        self.temporary.cleanup()

    def values(self):
        with closing(sqlite3.connect(self.database)) as db:
            return dict(db.execute('SELECT key,value FROM tunables'))

    def test_debug_preserves_originals_repeat_start_and_disabled_choice(self):
        original = {'bite_delay_max': '23', 'fish_weight_scale': '1.25', 'fish_pull_scale': '0.7'}
        with closing(sqlite3.connect(self.database)) as db, db:
            db.executemany('INSERT INTO tunables(key,value) VALUES(?,?)', original.items())
            schema_before = db.execute('SELECT name,sql FROM sqlite_master ORDER BY name').fetchall()
            version_before = db.execute('PRAGMA user_version').fetchone()
        enabled = ensure_debug_defaults(self.database)
        self.assertTrue(enabled['enabled'])
        self.assertEqual((enabled['biteMinSeconds'], enabled['biteMaxSeconds'], enabled['weightMultiplier']), (3, 5, 4))
        values = self.values()
        self.assertEqual({key: values[key] for key in DEBUG_VALUES}, DEBUG_VALUES)
        self.assertEqual(values['fish_pull_scale'], '0.7')
        ensure_debug_defaults(self.database)
        self.assertEqual(self.values(), values)
        disabled = set_debug(self.database, False)
        self.assertFalse(disabled['enabled'])
        restored = self.values()
        self.assertEqual({k: v for k, v in restored.items() if k not in ('debug_mode', DEBUG_STATE_KEY)}, original)
        ensure_debug_defaults(self.database)
        self.assertEqual(self.values(), restored)
        with closing(sqlite3.connect(self.database)) as db:
            self.assertEqual(db.execute('SELECT name,sql FROM sqlite_master ORDER BY name').fetchall(), schema_before)
            self.assertEqual(db.execute('PRAGMA user_version').fetchone(), version_before)
            self.assertEqual(db.execute('PRAGMA integrity_check').fetchone()[0], 'ok')

    def test_debug_preset_upgrade_preserves_original_settings(self):
        original = {'fish_weight_scale': '1.25', 'bite_delay_max': '23'}
        with closing(sqlite3.connect(self.database)) as db, db:
            db.executemany('INSERT INTO tunables(key,value) VALUES(?,?)', original.items())
        set_debug(self.database, True)
        with closing(sqlite3.connect(self.database)) as db, db:
            db.execute("UPDATE tunables SET value='2' WHERE key='fish_weight_scale'")
            db.execute("UPDATE tunables SET value='10' WHERE key='bite_delay_max'")
        enabled = ensure_debug_defaults(self.database)
        self.assertEqual((enabled['biteMinSeconds'], enabled['biteMaxSeconds'], enabled['weightMultiplier']), (3, 5, 4))
        set_debug(self.database, False)
        values = self.values()
        self.assertEqual({key: values[key] for key in original}, original)

    def test_debug_write_failure_rolls_back_every_preset_value(self):
        with closing(sqlite3.connect(self.database)) as db, db:
            db.execute("INSERT INTO tunables(key,value) VALUES('fish_weight_scale','1.7')")
            db.execute("CREATE TRIGGER reject_weight BEFORE UPDATE ON tunables "
                       "WHEN NEW.key='fish_weight_scale' BEGIN SELECT RAISE(ABORT,'blocked'); END")
        before = self.values()
        with self.assertRaises(LocalOptionsError) as failure:
            ensure_debug_defaults(self.database)
        self.assertEqual(failure.exception.status, 503)
        self.assertEqual(self.values(), before)

    def test_debug_invalid_flag_and_corrupt_state_do_not_overwrite(self):
        for invalid in (0, 1, 'true', None, []):
            with self.subTest(value=invalid), self.assertRaises(LocalOptionsError) as failure:
                set_debug(self.database, invalid)
            self.assertEqual(failure.exception.status, 400)
        self.assertFalse(self.values())
        with closing(sqlite3.connect(self.database)) as db, db:
            db.execute('INSERT INTO tunables(key,value) VALUES(?,?)', (DEBUG_STATE_KEY, '{broken'))
        before = self.values()
        with self.assertRaises(LocalOptionsError):
            ensure_debug_defaults(self.database)
        self.assertEqual(self.values(), before)
        missing = self.directory / 'missing.sqlite3'
        with self.assertRaises(LocalOptionsError):
            debug_status(missing)
        self.assertFalse(missing.exists())

    def test_real_membership_purchase_replay_and_identity(self):
        before = membership(self.database, self.reference, 7357)
        self.assertEqual(before['balance']['gold'], 10000)
        self.assertFalse(before['membership']['active'])
        request_id = str(uuid4())
        first = membership(self.database, self.reference, 7357, plan_id='premium_1d', request_id=request_id)
        second = membership(self.database, self.reference, 7357, plan_id='premium_1d', request_id=request_id)
        self.assertEqual(first['balance']['gold'], 9900)
        self.assertEqual(second['balance']['gold'], 9900)
        self.assertTrue(second['purchase']['replayed'])
        self.assertEqual(second['membership']['xpBonusRate'], 1)
        self.assertEqual(membership(self.database, self.reference, 2)['balance']['gold'], 60000)
        with self.assertRaises(LocalOptionsError) as failure:
            membership(self.database, self.reference, 7357, plan_id='premium_7d', request_id=request_id)
        self.assertEqual(failure.exception.status, 409)
        self.assertEqual(membership(self.database, self.reference, 7357)['balance']['gold'], 9900)

    def test_membership_boundaries_and_failed_purchase_do_not_create_values(self):
        for plan, request in (([], str(uuid4())), ('unknown', str(uuid4())), ('premium_1d', 'invalid')):
            with self.subTest(plan=plan), self.assertRaises(LocalOptionsError) as failure:
                membership(self.database, self.reference, 7357, plan_id=plan, request_id=request)
            self.assertEqual(failure.exception.status, 400)
        with self.assertRaises(LocalOptionsError) as failure:
            membership(self.database, self.reference, True)
        self.assertEqual(failure.exception.status, 403)
        with closing(sqlite3.connect(self.database)) as db, db:
            db.execute('UPDATE profile SET gold=0 WHERE uid=7357')
        with self.assertRaises(LocalOptionsError) as failure:
            membership(self.database, self.reference, 7357, plan_id='premium_1d', request_id=str(uuid4()))
        self.assertEqual(failure.exception.status, 409)
        state = membership(self.database, self.reference, 7357)
        self.assertEqual(state['balance']['gold'], 0)
        self.assertFalse(state['membership']['active'])
        bad = subprocess.CompletedProcess([], 0, '{"ok":true,"balance":{"gold":123456}}')
        with patch('rf4_local_options.subprocess.run', return_value=bad), self.assertRaises(LocalOptionsError) as failure:
            membership(self.database, self.reference, 7357)
        self.assertEqual(failure.exception.status, 503)

    def test_http_permissions_server_bound_uid_and_repeated_purchase(self):
        auth = self.directory / 'local.sqlite3'
        server.initialize(auth)
        with server.connect(auth) as db:
            for name, token in [('123123', 'owner-token'), ('other', 'other-token')]:
                db.execute('INSERT INTO accounts(username,salt,digest) VALUES(?,?,?)', (name, b'salt', server.password_digest('123123', b'salt')))
                db.execute('INSERT INTO sessions VALUES(?,?,?)',
                           (hashlib.sha256(token.encode()).hexdigest(), name, 2**31))
        with server.connect(auth) as db:
            db.execute('INSERT INTO accounts(rowid,username,salt,digest) VALUES(?,?,?,?)',
                       (7357, 'unbound', b'salt', server.password_digest('123123', b'salt')))
            db.execute('INSERT INTO sessions VALUES(?,?,?)',
                       (hashlib.sha256(b'unbound-token').hexdigest(), 'unbound', 2**31))
        ensure_debug_defaults(self.database)
        http_server = server.Server(('127.0.0.1', 0), server.Handler)
        http_server.db_path = auth
        http_server.character_user_ids = {'123123': 7357}
        http_server.backend_process = SimpleNamespace(reference_dir=self.reference)
        http_server.events_lock = threading.Lock()
        http_server.client_events = deque(maxlen=100)
        game = server.GameServer(('127.0.0.1', 0), server.GameHandler)
        game.http = http_server
        http_server.game_port = game.server_address[1]
        threads = [threading.Thread(target=instance.serve_forever, daemon=True) for instance in (http_server, game)]
        for thread in threads:
            thread.start()

        def request(method, path, data=None, token=None, origin=None, raw=False):
            connection = http.client.HTTPConnection('127.0.0.1', http_server.server_port, timeout=15)
            try:
                headers = {'Content-Type': 'application/json'} if data is not None else {}
                if token: headers['Authorization'] = 'Bearer ' + token
                if origin: headers['Origin'] = origin
                connection.request(method, path, json.dumps(data) if data is not None else None, headers)
                response = connection.getresponse()
                body = response.read()
                return response.status, body if raw else json.loads(body)
            finally:
                connection.close()
        try:
            purchase = {'planID': 'premium_1d', 'requestID': str(uuid4())}
            self.assertEqual(request('GET', '/api/profile')[0], 401)
            self.assertEqual(request('GET', '/api/profile', token='unbound-token')[0], 403)
            self.assertEqual(request('GET', '/api/profile?uid=2', token='owner-token')[0], 400)
            status, summary = request('GET', '/api/profile', token='owner-token')
            self.assertEqual((status, summary['userId'], summary['goldMinor']), (200, 7357, 10000))
            self.assertEqual(request('GET', '/api/profile', token='other-token')[1]['userId'], 2)
            status, html = request('GET', '/portal/', raw=True)
            self.assertEqual(status, 200)
            self.assertIn('本地高级会员'.encode(), html)
            self.assertIn(b'/portal/local.js', html)
            self.assertEqual(request('GET', '/api/membership')[0], 401)
            self.assertEqual(request('GET', '/api/membership', token='unbound-token')[0], 403)
            with server.connect(auth) as db:
                session_count = db.execute('SELECT COUNT(*) FROM sessions').fetchone()[0]
            query = urlencode({'login': 'unbound', 'password': '123123', 'version': '4.0.25034'})
            status, response = request('GET', '/lv58/https://api.rf4game.ru/login.php?' + query, raw=True)
            self.assertEqual((status, ET.fromstring(response).get('success')), (200, 'false'))
            with server.connect(auth) as db:
                self.assertEqual(db.execute('SELECT COUNT(*) FROM sessions').fetchone()[0], session_count)
            unbound = Client(game.server_address[1], 'unbound-token')
            try:
                self.assertFalse(unbound.accepted)
            finally:
                unbound.close()
            for token in ('owner-token', 'other-token'):
                valid = Client(game.server_address[1], token)
                try:
                    self.assertTrue(valid.accepted)
                finally:
                    valid.close()
            self.assertEqual(request('POST', '/api/membership/purchase', purchase)[0], 401)
            self.assertEqual(request('PUT', '/api/debug', {'enabled': False})[0], 401)
            self.assertEqual(request('GET', '/api/debug')[0], 200)
            self.assertEqual(request('GET', '/api/membership', token='owner-token', origin='https://elsewhere.invalid')[0], 403)
            self.assertEqual(request('POST', '/api/membership/purchase', dict(purchase, uid=2), 'owner-token')[0], 400)
            self.assertEqual(request('GET', '/api/membership?uid=2', token='owner-token')[0], 400)
            self.assertEqual(request('PUT', '/api/debug', {'enabled': 1}, 'owner-token')[0], 400)
            self.assertEqual(request('PUT', '/api/debug', {'enabled': True, 'fish_weight_scale': 99}, 'owner-token')[0], 400)
            self.assertFalse(request('PUT', '/api/debug', {'enabled': False}, 'owner-token')[1]['enabled'])
            self.assertTrue(request('PUT', '/api/debug', {'enabled': True}, 'owner-token')[1]['enabled'])
            status, state = request('GET', '/api/membership', token='owner-token')
            self.assertEqual((status, state['balance']['gold']), (200, 10000))
            self.assertEqual(request('GET', '/api/membership', token='other-token')[1]['balance']['gold'], 60000)
            status, bought = request('POST', '/api/membership/purchase', purchase, 'owner-token')
            self.assertEqual((status, bought['balance']['gold']), (200, 9900))
            status, replay = request('POST', '/api/membership/purchase', purchase, 'owner-token')
            self.assertEqual((status, replay['balance']['gold'], replay['purchase']['replayed']), (200, 9900, True))
        finally:
            for instance in (http_server, game):
                instance.shutdown()
                instance.server_close()
            for thread in threads:
                thread.join(timeout=2)


if __name__ == '__main__':
    unittest.main(verbosity=2)
