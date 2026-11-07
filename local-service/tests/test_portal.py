"""Exercise the local portal with isolated synthetic accounts and game data."""
from collections import deque
from contextlib import closing
import hashlib
import http.client
import json
from pathlib import Path
import sqlite3
import sys
import tempfile
import threading
import time
import unittest
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import local_server as server
from rf4_portal import asset, PortalMixin
from rf4_local_options import LocalOptionsError


class PortalTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='rf4-portal-')
        self.root = Path(self.temp.name)
        self.http = server.Server(('127.0.0.1', 0), server.Handler)
        self.http.db_path = self.root / 'local.sqlite3'
        self.http.character_user_ids = {'player_one': 42, 'player_two': 43}
        self.http.client_events, self.http.events_lock = deque(maxlen=10), threading.Lock()
        self.http.game_port = 0
        server.initialize(self.http.db_path)
        with server.connect(self.http.db_path) as db:
            for name in self.http.character_user_ids:
                db.execute('INSERT INTO accounts(username,salt,digest) VALUES(?,?,?)', (name,b'x',b'y'))
                db.execute('INSERT INTO sessions VALUES(?,?,?)',
                    (hashlib.sha256(name.encode()).hexdigest(), name, int(time.time())+600))
        with closing(sqlite3.connect(self.root/'gameplay.sqlite3')) as db:
            db.execute('CREATE TABLE profile(uid INTEGER PRIMARY KEY,name TEXT,level INTEGER,stars INTEGER,region TEXT,'
                       'gold INTEGER,silver INTEGER,membership INTEGER,membership_exp INTEGER)')
            db.executemany('INSERT INTO profile VALUES(?,?,?,?,?,?,?,?,?)',
                           [(42,'测试角色',20,1,'中国',2000,12345,2,0),(43,'另一个角色',4,0,'中国',100,300,1,1)])
            db.commit()
        self.thread = threading.Thread(target=self.http.serve_forever,daemon=True)
        self.thread.start()

    def tearDown(self):
        self.http.shutdown(); self.http.server_close(); self.thread.join()
        self.temp.cleanup()

    def request(self, method, path, token='player_one', data=None, **extra):
        headers={'Authorization':'Bearer '+token, **extra}
        if data is not None: headers['Content-Type']='application/json'
        connection=http.client.HTTPConnection('127.0.0.1',self.http.server_port,timeout=3)
        try:
            connection.request(method,path,body=json.dumps(data) if data is not None else None,headers=headers)
            response=connection.getresponse()
            return response.status,response.read()
        finally:
            connection.close()

    def test_profile_identity_is_session_bound_and_read_only(self):
        before=(self.root/'gameplay.sqlite3').read_bytes()
        status,raw=self.request('GET','/api/profile')
        self.assertEqual(status,200)
        value=json.loads(raw)
        self.assertEqual((value['userId'],value['nickname'],value['goldMinor'],value['premium']),
                         (42,'测试角色',2000,True))
        status,raw=self.request('GET','/portal/api/profile','player_two')
        self.assertEqual((status,json.loads(raw)['userId'],json.loads(raw)['premium']),(200,43,False))
        self.assertEqual(self.request('GET','/api/profile?uid=43')[0],400)
        self.assertEqual(self.request('POST','/api/profile')[0],405)
        self.assertEqual(self.request('GET','/api/profile','missing')[0],401)
        self.assertEqual((self.root/'gameplay.sqlite3').read_bytes(),before)

    def test_loopback_origin_and_missing_database(self):
        self.assertEqual(self.request('GET','/api/profile',Origin='https://example.invalid')[0],403)
        self.assertEqual(self.request('GET','/portal/',Host='example.invalid')[0],403)
        fake=type('Fake',(),{'client_address':('192.0.2.10',0)})()
        with self.assertRaises(LocalOptionsError): PortalMixin.portal_local(fake)
        (self.root/'gameplay.sqlite3').unlink()
        self.assertEqual(self.request('GET','/api/profile')[0],503)
        self.assertFalse((self.root/'gameplay.sqlite3').exists())

    def test_assets_reuse_main_css_and_deny_traversal(self):
        shared=Path(__file__).resolve().parents[2]/'internal/portal/web'
        for name in ('framework.css','portal.css','reference-ui.css','local.css'):
            status,body,kind=asset('/portal/'+name)
            self.assertEqual(status,200)
            self.assertEqual(body,(shared/name).read_bytes())
            self.assertIn('text/css',kind)
        status,body,_=asset('/portal/')
        self.assertEqual(status,200)
        self.assertIn(b'name="username" autocomplete="username"',body)
        self.assertNotIn(b'local-debug',body)
        for path in ('/portal/../local_server.py','/portal/%2e%2e/data/local.sqlite3','/portal/missing.js'):
            self.assertEqual(asset(path)[0],404)
        self.assertIsNone(asset('/portal/api/profile'))
