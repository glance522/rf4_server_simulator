"""The local startup contract and shutdown only affect this service's owned children."""
from contextlib import redirect_stdout
import http.client
from io import StringIO
import json
import os
from pathlib import Path
import sys
import tempfile
import threading
import time
import unittest
from unittest.mock import patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import local_server
from rf4_service_process import BackendProcess


class LifecycleTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory(prefix='rf4-lifecycle-')
        self.addCleanup(self.temp.cleanup)
        self.root=Path(self.temp.name)
        self.server=local_server.Server(('127.0.0.1',0),local_server.Handler)
        self.server.db_path=self.root/'local.sqlite3'
        self.server.game_port=0
        self.thread=threading.Thread(target=self.server.serve_forever,daemon=True)
        self.thread.start()
        self.addCleanup(self.close_server)

    def close_server(self):
        self.server.shutdown();self.server.server_close();self.thread.join(timeout=3)

    def request(self,method,path,data=None,server=None,headers=None):
        server=server or self.server
        values={'Content-Type':'application/json'} if data is not None else {}
        values.update(headers or {})
        client=http.client.HTTPConnection('127.0.0.1',server.server_port,timeout=3)
        try:
            client.request(method,path,json.dumps(data) if data is not None else None,headers=values)
            response=client.getresponse()
            return response.status,json.loads(response.read())
        finally:client.close()

    def test_health_identifies_current_save_and_rotates_control_per_instance(self):
        status,health=self.request('GET','/api/health')
        self.assertEqual(status,200)
        self.assertEqual(health['saveRoot'],str(self.root.resolve()))
        self.assertEqual(health['control'],self.server.control)
        self.assertEqual(self.request('GET','/api/health')[1]['control'],health['control'])
        self.assertEqual(set(health),{'app','status','gameProtocol','nativeAuthentication','gamePort','saveRoot','control'})
        other=local_server.Server(('127.0.0.1',0),local_server.Handler)
        try:self.assertNotEqual(other.control,health['control'])
        finally:other.server_close()

    def test_wrong_missing_or_cross_site_control_cannot_stop_the_service(self):
        for data in ({},{'control':'wrong'},{'control':True},{'control':'非ASCII'}):
            self.assertEqual(self.request('POST','/api/shutdown',data)[0],403)
        valid={'control':self.server.control}
        for headers in ({'Host':'example.invalid'},{'Origin':'https://example.invalid'},
                        {'Sec-Fetch-Site':'cross-site'},{'Sec-Fetch-Site':'same-site'}):
            self.assertEqual(self.request('POST','/api/shutdown',valid,headers=headers)[0],403)
        self.assertEqual(self.request('POST','/api/shutdown',valid,headers={'Content-Type':'text/plain'})[0],415)
        self.assertEqual(self.request('GET','/api/health')[0],200)

    def test_lifecycle_rejects_a_non_loopback_peer(self):
        class RemotePeer(local_server.Handler):
            def dispatch(self):
                self.client_address=('192.0.2.1',self.client_address[1])
                return super().dispatch()
        other=local_server.Server(('127.0.0.1',0),RemotePeer)
        thread=threading.Thread(target=other.serve_forever,daemon=True);thread.start()
        try:
            self.assertEqual(self.request('GET','/api/health',server=other)[0],403)
            self.assertEqual(self.request('POST','/api/shutdown',{'control':other.control},server=other)[0],403)
        finally:other.shutdown();other.server_close();thread.join(timeout=3)

    @unittest.skipUnless(os.environ.get('RF4_TEST_BACKEND_REFERENCE'),'Requires built gameserver/launcher and read-only static resources')
    def test_valid_shutdown_closes_only_the_owned_real_go_children(self):
        reference=Path(os.environ['RF4_TEST_BACKEND_REFERENCE'])
        servers=[];managers=[];errors=[];output=StringIO()
        original_server=local_server.Server
        def make_server(*args,**kwargs):
            value=original_server(*args,**kwargs);servers.append(value);return value
        def make_manager(data_dir,**kwargs):
            value=BackendProcess(data_dir,reference_dir=reference,**kwargs);managers.append(value);return value
        def run():
            try:local_server.main()
            except BaseException as error:errors.append(error)
        argv=['local_server.py','--port','0','--game-port','0','--data-dir',str(self.root/'owned'),
              '--managed-backend','--backend-timeout','10']
        children=[];config=None
        with patch.object(sys,'argv',argv),patch.object(local_server,'Server',make_server), \
                patch('rf4_service_process.BackendProcess',make_manager),redirect_stdout(output):
            thread=threading.Thread(target=run,daemon=True);thread.start()
            try:
                deadline=time.monotonic()+15
                while not output.getvalue() and not errors and time.monotonic()<deadline:time.sleep(.02)
                self.assertFalse(errors,errors)
                self.assertTrue(output.getvalue(),'managed local service did not become ready')
                owned=servers[0];manager=managers[0]
                children=[manager.process,manager.admin_process];config=Path(manager._temporary.name)
                status,health=self.request('GET','/api/health',server=owned)
                self.assertEqual(status,200)
                self.assertEqual(health['saveRoot'],str((self.root/'owned').resolve()))
                self.assertNotEqual(health['control'],self.server.control)
                self.assertEqual(self.request('POST','/api/shutdown',{'control':self.server.control},server=owned)[0],403)
                self.assertEqual(self.request('POST','/api/shutdown',{'control':health['control']},server=owned),
                                 (200,{'status':'stopping'}))
                thread.join(timeout=8)
                self.assertFalse(thread.is_alive(),'managed local service did not stop')
            finally:
                if thread.is_alive() and servers:
                    threading.Thread(target=servers[0].shutdown,daemon=True).start();thread.join(timeout=12)
                for manager in managers:manager.close()
        self.assertFalse(errors,errors)
        self.assertEqual([child.poll() for child in children],[0,0])
        self.assertFalse(config.exists())
        self.assertEqual(self.request('GET','/api/health')[0],200)
