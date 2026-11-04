"""Exercise owned-process lifecycle with an isolated Python tunnel peer."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import types
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from rf4_service_process import BackendProcess


FAKE_SERVER = r'''
import argparse,hmac,json,os,socket,sys,threading
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
p=argparse.ArgumentParser();p.add_argument('-config');p.add_argument('-parent-stdin',action='store_true');p.add_argument('-managed-admin',action='store_true');p.add_argument('-panel-addr')
args=p.parse_args();cfg=json.load(open(args.config,encoding='utf-8'))
mode=os.environ.get('RF4_TEST_CHILD_MODE','ready')
if mode=='exit':sys.exit(7)
if args.managed_admin:
    if mode=='admin-exit':sys.exit(9)
    class AdminHandler(BaseHTTPRequestHandler):
        def log_message(self,*args):pass
        def do_GET(self):
            self.send_response(200);self.end_headers();self.wfile.write(b'{"admin_ready":true}')
    h,port=args.panel_addr.split(':');server=ThreadingHTTPServer((h,int(port)),AdminHandler)
    threading.Thread(target=server.serve_forever,daemon=True).start()
    sys.stdin.buffer.read();server.shutdown();server.server_close();sys.exit(0)
key=os.environ['RF4_GAME_TUNNEL_PSK'].encode()
host,port=cfg['game']['tunnel']['addr'].split(':');listener=socket.socket()
listener.bind((host,int(port)));listener.listen(2);listener.settimeout(.2)
stop=threading.Event()
def tunnels():
    while not stop.is_set():
        try:connection,_=listener.accept()
        except socket.timeout:continue
        except OSError:return
        with connection:
            connection.settimeout(.3)
            try:
                request=b''
                while len(request)<37:
                    chunk=connection.recv(37-len(request))
                    if not chunk:break
                    request+=chunk
                assert request==b'RF4T1'+hmac.digest(key,b'RF4T1\0gateway','sha256')
                actual=key if mode!='wrong-key' else key+b'wrong'
                connection.sendall(b'RF4T1'+hmac.digest(actual,b'RF4T1\0gameserver','sha256'))
            except OSError:pass
class Handler(BaseHTTPRequestHandler):
    def log_message(self,*args):pass
    def do_GET(self):
        ready=mode!='not-ready'
        self.send_response(200 if ready else 503);self.end_headers()
        self.wfile.write(b'ready\n' if ready else b'not ready\n')
obs_host,obs_port=cfg['game']['obs_addr'].split(':')
http=ThreadingHTTPServer((obs_host,int(obs_port)),Handler)
threading.Thread(target=tunnels,daemon=True).start()
threading.Thread(target=http.serve_forever,daemon=True).start()
def parent():
    sys.stdin.buffer.read()
    if mode!='ignore-eof':stop.set()
threading.Thread(target=parent,daemon=True).start()
stop.wait();http.shutdown();http.server_close();listener.close()
'''


class ProcessTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='rf4-managed-test-')
        self.root = Path(self.temporary.name)
        self.reference = self.root / 'reference'
        self.data = self.root / 'data'
        for path in (self.reference / 'bin' / 'gameserver.exe',
                     self.reference / 'bin' / 'launcher.exe',
                     self.reference / 'bin' / 'import-character.exe',
                     self.reference / 'server_data' / 'static.sqlite3'):
            path.parent.mkdir(parents=True, exist_ok=True)
            path.touch()
        self.character = self.data / 'characters' / 'sample'
        self.character.mkdir(parents=True)
        (self.character / 'inventory.json').write_text(json.dumps({
            'schema': 2, 'clientVersion': '4.0.25034', 'targetAccount': 'sample',
            'sourceUserID': 101, 'idsByKind': {'0': ['first', 'second']}}), encoding='utf-8')
        self.instances = []
        self.children = []
        self.launches = []
        self.real_popen = subprocess.Popen
        self.mode = 'ready'
        self.imported = {'uid': 101, 'items': 2, 'dryRun': False, 'alreadyImported': False}
        self.import_result = subprocess.CompletedProcess([], 0, json.dumps(self.imported), '')

        def launch(command, **options):
            self.launches.append((list(command), dict(options)))
            environment = dict(options['env'], RF4_TEST_CHILD_MODE=self.mode)
            options['env'] = environment
            child = self.real_popen([sys.executable, '-B', '-u', '-c', FAKE_SERVER, *command[1:]],
                                    **options)
            self.children.append(child)
            return child

        self.popen_patch = patch('rf4_service_process.subprocess.Popen', side_effect=launch)
        self.import_patch = patch('rf4_service_process.subprocess.run', side_effect=lambda *a, **k: self.import_result)
        self.backend_patch = patch.dict(sys.modules, {'rf4_backend': types.SimpleNamespace(MAGIC=b'RF4T1')})
        self.backend_patch.start()
        self.popen_patch.start()
        self.importer = self.import_patch.start()

    def tearDown(self):
        try:
            for instance in self.instances:
                instance.close()
            for child in self.children:
                if child.poll() is None:
                    child.kill()
                    child.wait(timeout=2)
                if child.stdin is not None and not child.stdin.closed:
                    child.stdin.close()
        finally:
            self.popen_patch.stop()
            self.import_patch.stop()
            self.backend_patch.stop()
            self.temporary.cleanup()

    def instance(self, startup_timeout=3, shutdown_timeout=2, import_character=True):
        owner = BackendProcess(self.data, self.reference, startup_timeout, shutdown_timeout,
                               character_dir=self.character if import_character else None)
        self.instances.append(owner)
        return owner

    def test_start_checks_ready_identity_and_cleans_config_on_eof(self):
        with patch.dict(os.environ, {'RF4_SQLITE_PATH': 'must-not-use.sqlite3',
                                    'RF4_GAME_TUNNEL_PSK': 'inherited-key'}):
            owner = self.instance().start()
        child = owner.process
        command, options = self.launches[0]
        config_path = Path(command[command.index('-config') + 1])
        raw = config_path.read_text(encoding='utf-8')
        config = json.loads(raw)
        self.assertFalse(owner.psk in raw)
        self.assertFalse(owner.psk in ' '.join(command))
        self.assertFalse('RF4_SQLITE_PATH' in options['env'])
        self.assertEqual(options['env']['RF4_GAME_TUNNEL_PSK'], owner.psk)
        self.assertTrue('-parent-stdin' in command)
        self.assertEqual(config['game']['tunnel']['addr'], f'127.0.0.1:{owner.address[1]}')
        self.assertTrue(config['game']['obs_addr'].startswith('127.0.0.1:'))
        self.assertEqual(Path(config['sqlite']['path']), self.data / 'gameplay.sqlite3')
        self.assertEqual(Path(config['sqlite']['static_path']), self.reference / 'server_data' / 'static.sqlite3')
        self.assertNotIn('default_userid', config['game'])
        owner.check_alive()
        owner.close()
        self.assertEqual(child.poll(), 0)
        self.assertFalse(config_path.exists())
        self.assertTrue(child.stdin.closed)
        self.assertIsNone(owner.address)
        self.assertIsNone(owner.admin_address)
        self.assertIsNone(owner.psk)
        owner.close()

    def test_without_explicit_character_does_not_import_or_require_importer(self):
        (self.reference / 'bin' / 'import-character.exe').unlink()
        owner = self.instance(import_character=False).start()
        self.importer.assert_not_called()
        self.assertFalse(owner.database.exists())
        owner.check_alive()

    def test_explicit_character_requires_importer_before_starting_children(self):
        (self.reference / 'bin' / 'import-character.exe').unlink()
        with self.assertRaisesRegex(RuntimeError, 'import-character.exe'):
            self.instance().start()
        self.assertFalse(self.launches)

    def test_admin_password_is_configuration_only(self):
        password = 'synthetic-new-admin-password'
        with patch.dict(os.environ, {'RF4_LOCAL_ADMIN_PASSWORD': password}):
            self.instance(import_character=False).start()
        command, options = self.launches[1]
        config = json.loads(Path(command[command.index('-config') + 1]).read_text(encoding='utf-8'))
        self.assertEqual(config['admin']['password'], password)
        self.assertNotIn('RF4_LOCAL_ADMIN_PASSWORD', options['env'])
        self.assertNotIn(password, command)

    def test_live_start_is_idempotent_and_new_start_uses_new_key(self):
        owner = self.instance().start()
        first_process, first_key = owner.process, owner.psk
        owner.start()
        self.assertEqual(len(self.launches), 2)
        self.assertEqual(self.importer.call_count, 1)
        self.assertIs(owner.process, first_process)
        owner.close()
        self.import_result = subprocess.CompletedProcess([], 0,
            json.dumps(dict(self.imported, alreadyImported=True)), '')
        owner.start()
        self.assertNotEqual(owner.psk, first_key)
        self.assertEqual(len(self.launches), 4)
        self.assertEqual(self.importer.call_count, 2)

    def test_import_failure_prevents_backend_start(self):
        self.import_result = subprocess.CompletedProcess([], 1, '', 'invalid snapshot')
        with self.assertRaisesRegex(RuntimeError, 'invalid snapshot'):
            self.instance().start()
        self.assertFalse(self.launches)

    def test_import_identity_mismatch_prevents_backend_start(self):
        self.import_result = subprocess.CompletedProcess([], 0,
            json.dumps(dict(self.imported, uid=999)), '')
        with self.assertRaisesRegex(RuntimeError, 'does not match'):
            self.instance().start()
        self.assertFalse(self.launches)

    def test_early_process_exit_is_reported_without_waiting_for_deadline(self):
        self.mode = 'exit'
        start = time.monotonic()
        with self.assertRaisesRegex(RuntimeError, 'code 7'):
            self.instance().start()
        self.assertLess(time.monotonic() - start, 2)
        self.assertTrue(self.children[0].stdin.closed)

    def test_ready_without_correct_tunnel_key_is_rejected(self):
        self.mode = 'wrong-key'
        with self.assertRaisesRegex(RuntimeError, 'did not become ready'):
            self.instance(startup_timeout=.4).start()
        self.assertEqual(self.children[0].poll(), 0)

    def test_readiness_timeout_shuts_down_the_owned_child(self):
        self.mode = 'not-ready'
        with self.assertRaisesRegex(RuntimeError, 'did not become ready'):
            self.instance(startup_timeout=.4).start()
        self.assertEqual(self.children[0].poll(), 0)

    def test_unresponsive_child_is_terminated_without_stopping_other_process(self):
        self.mode = 'ignore-eof'
        owner = self.instance(shutdown_timeout=.05).start()
        child = owner.process
        other = self.real_popen([sys.executable, '-B', '-c', 'import sys;sys.stdin.buffer.read()'],
                                stdin=subprocess.PIPE, stdout=subprocess.DEVNULL,
                                stderr=subprocess.DEVNULL,
                                creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
        try:
            with patch.object(child, 'terminate', wraps=child.terminate) as terminate:
                owner.close()
                terminate.assert_called_once()
            self.assertIsNotNone(child.poll())
            self.assertIsNone(other.poll())
        finally:
            other.stdin.close()
            other.wait(timeout=2)

    def test_admin_exit_stops_owned_game_process(self):
        self.mode = 'admin-exit'
        owner = self.instance()
        with self.assertRaisesRegex(RuntimeError, 'administration process exited'):
            owner.start()
        self.assertTrue(all(child.poll() is not None for child in self.children))

    def test_later_child_exit_is_reported(self):
        owner = self.instance().start()
        owner.process.terminate()
        owner.process.wait(timeout=2)
        with self.assertRaisesRegex(RuntimeError, 'exited'):
            owner.check_alive()

    def test_start_replaces_owned_children_after_admin_exit(self):
        owner = self.instance().start()
        previous = owner.process
        owner.admin_process.terminate()
        owner.admin_process.wait(timeout=2)
        owner.start()
        self.assertIsNot(owner.process, previous)
        self.assertIsNotNone(previous.poll())
        self.assertEqual(len(self.launches), 4)
        owner.check_alive()


if __name__ == '__main__':
    unittest.main(verbosity=2)
