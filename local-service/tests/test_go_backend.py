"""The local protocol bridge against a freshly built Go service and isolated save."""
from contextlib import closing
import json
import os
from pathlib import Path
import secrets
import socket
import sqlite3
import struct
import subprocess
import tempfile
import time
import unittest
import urllib.request

from test_gateway_bridge import gateway
from test_local_server import Client, ROOT, decode


class GoBackendTests(unittest.TestCase):
    def test_real_go_profiles_remain_bound_to_separate_accounts(self):
        executable = Path(os.environ.get('RF4_TEST_GAMESERVER', ''))
        static = Path(os.environ.get('RF4_TEST_STATIC_DATABASE',
                                     ROOT.parent / 'server_data' / 'static.sqlite3'))
        if not executable.is_file() or not static.is_file():
            self.skipTest('Requires RF4_TEST_GAMESERVER and a read-only static database')

        def port():
            with socket.socket() as listener:
                listener.bind(('127.0.0.1', 0))
                return listener.getsockname()[1]

        game_port, observation_port = port(), port()
        while observation_port == game_port:
            observation_port = port()
        psk = secrets.token_hex(32)
        environment = {key: value for key, value in os.environ.items()
                       if not key.upper().startswith('RF4_')}
        environment['RF4_GAME_TUNNEL_PSK'] = psk
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with tempfile.TemporaryDirectory(prefix='rf4-go-bridge-') as temporary:
            directory = Path(temporary)
            database = directory / 'gameplay.sqlite3'
            config = directory / 'config.json'
            config.write_text(json.dumps({
                'log': {'level': 'error', 'format': 'json'},
                'sqlite': {'path': str(database), 'static_path': str(static.resolve())},
                'game': {'tunnel': {'addr': f'127.0.0.1:{game_port}'},
                         'obs_addr': f'127.0.0.1:{observation_port}',
                         'keepalive_interval_sec': 5},
            }), encoding='utf-8')
            process = subprocess.Popen(
                [str(executable.resolve()), '-config', str(config)], cwd=ROOT.parent,
                env=environment, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
            try:
                for _ in range(200):
                    self.assertIsNone(process.poll(), 'The isolated Go service exited early')
                    try:
                        with opener.open(f'http://127.0.0.1:{observation_port}/readyz', timeout=.2) as response:
                            if response.status == 200:
                                break
                    except OSError:
                        pass
                    time.sleep(.05)
                else:
                    self.fail('The isolated Go service did not become ready')
                for uid in (4242, 4243, 4242):
                    with gateway(('127.0.0.1', game_port), psk, user_id=uid) as (address, token):
                        client = Client(address, token)
                        try:
                            self.assertEqual(decode(client.call(10, (1, 1), ['4.0.25034'])), '4.0.25034')
                            profile = client.call(11, (3, 1))
                            self.assertEqual(profile[:5], b'\x01' + struct.pack('<i', 307))
                            self.assertEqual(struct.unpack_from('<I', profile, 5)[0], uid)
                        finally:
                            client.close()
                with closing(sqlite3.connect(database)) as db:
                    self.assertEqual(db.execute('SELECT uid FROM profile WHERE uid IN (4242,4243) ORDER BY uid').fetchall(),
                                     [(4242,), (4243,)])
            finally:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
