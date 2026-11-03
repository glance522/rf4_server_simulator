"""Real Go administration behind the local HTTP adapter; synthetic temporary databases."""
from collections import deque
from contextlib import closing
import http.client
import json
import os
from pathlib import Path
import socket
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
from types import SimpleNamespace
import unittest

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
import local_server as local
from rf4_local_options import ensure_debug_defaults


def free_port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


class AdminIntegrationTests(unittest.TestCase):
    def test_local_admin_auth_profile_debug_and_expiry(self):
        binary = Path(os.environ.get('RF4_TEST_GAMESERVER', ROOT.parent / 'bin/gameserver.exe'))
        if not binary.is_file():
            self.skipTest('Build gameserver or set RF4_TEST_GAMESERVER first')
        with tempfile.TemporaryDirectory(prefix='rf4-admin-integration-') as tmp:
            directory = Path(tmp)
            gameplay, auth = directory/'gameplay.sqlite3', directory/'local.sqlite3'
            ports = set()
            while len(ports) < 3:
                ports.add(free_port())
            admin_port, game_port, obs_port = ports
            config = {'env':'dev','log':{'level':'error'},'sqlite':{'path':str(gameplay),'static_path':str(ROOT.parent/'server_data/static.sqlite3')},
                      'game':{'tunnel':{'addr':f'127.0.0.1:{game_port}'},'obs_addr':f'127.0.0.1:{obs_port}'}}
            config_file=directory/'config.json'
            config_file.write_text(json.dumps(config),encoding='utf-8')
            env={k:v for k,v in os.environ.items() if not k.startswith('RF4_')}
            env.update(RF4_GAME_TUNNEL_PSK='integration-tunnel-key',RF4_LOCAL_ADMIN_ADDR=f'127.0.0.1:{admin_port}',
                       RF4_LOCAL_ADMIN_SECRET='integration-jwt-secret-0123456789abcdef',RF4_LOCAL_ADMIN_PASSWORD='123123')
            process=subprocess.Popen([str(binary),'-config',str(config_file),'-parent-stdin'],cwd=ROOT.parent,env=env,
                                     stdin=subprocess.PIPE,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,
                                     creationflags=getattr(subprocess,'CREATE_NO_WINDOW',0))
            server=None
            try:
                for _ in range(200):
                    if process.poll() is not None:
                        self.fail('Managed Go administration exited before ready')
                    try:
                        with socket.create_connection(('127.0.0.1',admin_port),timeout=.1):
                            break
                    except OSError:
                        time.sleep(.05)
                else:self.fail('Managed Go administration did not listen')
                with closing(sqlite3.connect(gameplay)) as db,db:
                    db.execute("INSERT INTO profile(uid,name,level,xp,gold) VALUES(7357,'测试角色',29,11675776,100000)")
                ensure_debug_defaults(gameplay)
                local.initialize(auth)
                with local.connect(auth) as db:
                    db.execute("INSERT INTO accounts(username,salt,digest) VALUES('123123',?,?)",(b'salt',local.password_digest('123123',b'salt')))
                server=local.Server(('127.0.0.1',0),local.Handler)
                server.db_path=auth
                server.character_user_ids={'123123':7357}
                server.backend_process=SimpleNamespace(admin_address=('127.0.0.1',admin_port),database=gameplay)
                server.events_lock=threading.Lock()
                server.client_events=deque(maxlen=100)
                thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
                def call(method,path,body=None,token='',origin=None):
                    with closing(http.client.HTTPConnection('127.0.0.1',server.server_port,timeout=10)) as c:
                        headers={'Content-Type':'application/json'}
                        if token:headers['Authorization']='Bearer '+token
                        if origin:headers['Origin']=origin
                        c.request(method,path,None if body is None else json.dumps(body),headers)
                        response=c.getresponse();raw=response.read()
                        return response.status, json.loads(raw) if 'application/json' in response.getheader('Content-Type','') else raw
                self.assertEqual(call('GET','/admin/api/accounts')[0],401)
                self.assertEqual(call('GET','/admin/api/debug')[0],401)
                self.assertEqual(call('POST','/admin/api/login',{'username':'admin','password':'wrong'})[0],401)
                status,data=call('POST','/admin/api/login',{'username':'admin','password':'123123'})
                self.assertEqual(status,200);token=data['token']
                status,html=call('GET','/admin/')
                self.assertEqual(status,200);self.assertIn('本地调试后台'.encode(),html)
                status,goods=call('GET','/admin/api/stores/items?store_id=fishingstore_009&q=braid_40907',token=token)
                self.assertEqual(status,200)
                self.assertEqual(len(goods),1)
                self.assertEqual((goods[0]['gold'],goods[0]['silver']),(1513,0))
                self.assertGreater(goods[0]['stock'],0)
                self.assertEqual(call('POST','/admin/api/stores/items/update',{'store_id':'fishingstore_009','item_id':'braid_40907','silver':0,'gold':1600,'stock':0},token)[0],200)
                status,goods=call('GET','/admin/api/stores/items?store_id=fishingstore_009&q=braid_40907',token=token)
                self.assertEqual((status,goods[0]['gold'],goods[0]['stock'],goods[0]['has_override']),(200,1600,1000,True))
                self.assertEqual(call('POST','/admin/api/stores/items/reset',{'store_id':'fishingstore_009','item_id':'braid_40907'},token)[0],200)
                status,accounts=call('GET','/admin/api/accounts',token=token)
                self.assertEqual((status,accounts),(200,[{'username':'123123','uid':7357,'passwordState':'已设置'}]))
                status,debug=call('GET','/admin/api/debug',token=token)
                self.assertEqual((status,debug['biteMinSeconds'],debug['biteMaxSeconds'],debug['weightMultiplier']),(200,3,5,4))
                self.assertEqual(call('POST','/admin/api/tunables',{'key':'fish_weight_scale','value':'99'},token)[0],409)
                self.assertEqual(call('PUT','/admin/api/debug',{'enabled':False},token)[0],200)
                self.assertEqual(call('GET','/admin/api/debug',token=token)[1]['weightMultiplier'],1)
                expiry=int(time.time())+86400
                self.assertEqual(call('POST','/admin/api/players/membership',{'uid':7357,'kind':1,'expiresAt':expiry},token)[0],200)
                status,profile=call('GET','/admin/api/players/detail?uid=7357',token=token)
                self.assertEqual((status,profile['gold'],profile['xp'],profile['membership_expires_at'],profile['membership_active']),(200,100000,11675776,expiry,True))
                self.assertEqual(call('POST','/admin/api/players/membership',{'uid':7357,'kind':1,'expiresAt':1},token)[0],400)
                self.assertEqual(call('GET','/admin/api/accounts',token=token,origin='https://other.invalid')[0],403)
                with closing(sqlite3.connect(gameplay)) as db:
                    self.assertEqual(db.execute('SELECT count(*) FROM membership_order').fetchone()[0],0)
            finally:
                if server:server.shutdown();server.server_close();thread.join(2)
                if process.stdin:process.stdin.close()
                try:process.wait(timeout=7)
                except subprocess.TimeoutExpired:process.terminate();process.wait(timeout=3)


if __name__=='__main__':
    unittest.main()
