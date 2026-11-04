"""Managed startup preserves account bindings before invoking the importer."""
from contextlib import redirect_stderr
from io import StringIO
import http.client
import json
from pathlib import Path
import sqlite3
import sys
import tempfile
import threading
import types
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import local_server


class FakeManager:
    instances = []
    failure = False

    def __init__(self, data_dir, **options):
        self.database = Path(data_dir) / 'gameplay.sqlite3'
        self.address, self.psk = ('127.0.0.1', 10101), 'synthetic-tunnel-key'
        self.started = self.closed = False
        self.options = options
        self.instances.append(self)

    def start(self):
        self.started = True
        if self.failure:
            raise RuntimeError('synthetic startup failure')
        return self

    def close(self):
        self.closed = True


class ManagedBackendTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='rf4-managed-binding-')
        self.root = Path(self.temporary.name)
        self.addCleanup(self.temporary.cleanup)
        self.db = sqlite3.connect(self.root / 'gameplay.sqlite3')
        self.addCleanup(self.db.close)
        self.db.execute('CREATE TABLE kv(k TEXT PRIMARY KEY,v TEXT NOT NULL)')
        self.db.execute('INSERT INTO kv VALUES(?,?)', ('native-import:101:inventory', json.dumps({
            'schema': 2, 'clientVersion': '4.0.25034', 'targetAccount': 'sample', 'sourceUserID': 101})))
        self.db.commit()
        FakeManager.instances, FakeManager.failure = [], False
        self.patch = patch('rf4_service_process.BackendProcess', FakeManager)
        self.patch.start()
        self.addCleanup(self.patch.stop)
        self.http = types.SimpleNamespace(character_user_ids={'sample': 101}, backend_config=None)

    def character(self, account, uid):
        directory = self.root / 'explicit-character'
        directory.mkdir(exist_ok=True)
        (directory / 'inventory.json').write_text(json.dumps({'targetAccount': account, 'sourceUserID': uid}), encoding='utf-8')
        return directory

    def test_existing_binding_is_preserved_without_import(self):
        owner = local_server.start_managed_backend(self.http, self.root)
        self.assertEqual(self.http.character_user_ids, {'sample': 101})
        self.assertIsNone(owner.options['character_dir'])
        self.assertEqual(self.http.backend_config, (owner.address, owner.psk))

    def test_explicit_new_binding_is_merged(self):
        local_server.start_managed_backend(self.http, self.root, self.character('other', 102))
        self.assertEqual(self.http.character_user_ids, {'sample': 101, 'other': 102})

    def test_identity_conflicts_are_rejected_before_import_or_child_start(self):
        for account, uid in [('sample', 102), ('other', 101)]:
            with self.subTest(account=account, uid=uid):
                with self.assertRaisesRegex(RuntimeError, '绑定'):
                    local_server.start_managed_backend(self.http, self.root, self.character(account, uid))
                owner = FakeManager.instances[-1]
                self.assertFalse(owner.started)
                self.assertTrue(owner.closed)
                self.assertEqual(self.http.character_user_ids, {'sample': 101})
                self.assertEqual(self.db.execute('SELECT COUNT(*) FROM kv').fetchone()[0], 1)

    def test_startup_failure_closes_owner_without_publishing_backend(self):
        FakeManager.failure = True
        with self.assertRaisesRegex(RuntimeError, 'synthetic startup failure'):
            local_server.start_managed_backend(self.http, self.root)
        self.assertTrue(FakeManager.instances[-1].closed)
        self.assertIsNone(self.http.backend_config)

    def test_character_option_requires_managed_mode(self):
        with patch.object(sys, 'argv', ['local_server.py', '--character-dir', str(self.root)]), redirect_stderr(StringIO()):
            with self.assertRaises(SystemExit) as failure:
                local_server.main()
        self.assertEqual(failure.exception.code, 2)

    def test_dead_owned_process_is_not_reported_ready(self):
        class DeadManager:
            def check_alive(self):
                raise RuntimeError('synthetic child exit')
        server = local_server.Server(('127.0.0.1', 0), local_server.Handler)
        server.backend_process = DeadManager()
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            client = http.client.HTTPConnection('127.0.0.1', server.server_port, timeout=2)
            client.request('GET', '/api/health')
            response = client.getresponse()
            self.assertEqual(response.status, 503)
            self.assertEqual(json.load(response)['status'], 'unavailable')
            client.close()
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)

    def test_health_provides_original_admin_login_url(self):
        manager = types.SimpleNamespace(admin_address=('127.0.0.1', 10102), check_alive=lambda: None)
        server = local_server.Server(('127.0.0.1', 0), local_server.Handler)
        server.db_path = self.root / 'local.sqlite3'
        server.backend_process, server.backend_config, server.game_port = manager, (('127.0.0.1', 10101), 'synthetic-key'), 10103
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            client = http.client.HTTPConnection('127.0.0.1', server.server_port, timeout=2)
            client.request('GET', '/api/health')
            response = client.getresponse()
            self.assertEqual(response.status, 200)
            self.assertEqual(json.load(response)['adminURL'], 'http://127.0.0.1:10102/admin/')
            client.close()
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)


if __name__ == '__main__':
    unittest.main()
