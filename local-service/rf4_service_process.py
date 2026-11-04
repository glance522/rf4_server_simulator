"""Start and stop only the game logic process owned by this local service."""
import hmac
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import time
import urllib.request

class BackendProcess:
    def __init__(self, data_dir, reference_dir=None, startup_timeout=30, shutdown_timeout=5,
                 character_dir=None):
        self.data_dir = Path(data_dir).resolve()
        self.reference_dir = (Path(reference_dir).resolve() if reference_dir is not None else
                              Path(__file__).resolve().parent.parent)
        if not 0 < startup_timeout <= 120 or not 0 < shutdown_timeout <= 5:
            raise ValueError('Invalid local backend startup or shutdown timeout')
        self.startup_timeout = startup_timeout
        self.shutdown_timeout = shutdown_timeout
        self.character_dir = Path(character_dir).resolve() if character_dir is not None else None
        self.database = self.data_dir / 'gameplay.sqlite3'
        self.address = None
        self.admin_address = None
        self.psk = None
        self.process = None
        self.admin_process = None
        self._temporary = None
        self._opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    @staticmethod
    def _child_environment():
        return {name: value for name, value in os.environ.items()
                if not name.upper().startswith('RF4_')}

    @staticmethod
    def _port():
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            return listener.getsockname()[1]

    def _import_character(self, environment):
        character_dir = self.character_dir
        index = json.loads((character_dir / 'inventory.json').read_text(encoding='utf-8'))
        uid = index.get('sourceUserID')
        if (index.get('schema') != 2 or not isinstance(index.get('targetAccount'), str)
                or not index['targetAccount'].strip()
                or index.get('clientVersion') != '4.0.25034'
                or type(uid) is not int or not 0 < uid <= 2**63 - 1):
            raise RuntimeError('Local character identity is invalid')
        expected_items = sum(len(values) for values in index['idsByKind'].values())
        command = [str(self.reference_dir / 'bin' / 'import-character.exe'),
                   '--character-dir', str(character_dir), '--database', str(self.database),
                   '--static-database', str(self.reference_dir / 'server_data' / 'static.sqlite3')]
        result = subprocess.run(command, cwd=self.reference_dir, env=environment,
                                stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                encoding='utf-8', errors='replace', timeout=120,
                                creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
        if result.returncode != 0:
            detail = result.stderr.strip()[:2000]
            raise RuntimeError('Local character import failed' + (': ' + detail if detail else ''))
        try:
            imported = json.loads(result.stdout)
        except ValueError:
            raise RuntimeError('The character importer returned an invalid result') from None
        if (type(imported.get('uid')) is not int or imported['uid'] != uid
                or imported.get('items') != expected_items or imported.get('dryRun') is not False
                or type(imported.get('alreadyImported')) is not bool):
            raise RuntimeError('The imported character does not match the local snapshot')
        return uid

    def _ready(self, port):
        try:
            with self._opener.open(f'http://127.0.0.1:{port}/readyz', timeout=.25) as response:
                return response.status == 200 and response.read(64) == b'ready\n'
        except OSError:
            return False

    def _verify_tunnel(self):
        from rf4_backend import MAGIC

        try:
            with socket.create_connection(self.address, timeout=.25) as connection:
                key = self.psk.encode()
                connection.sendall(MAGIC + hmac.digest(key, MAGIC + b'\0gateway', 'sha256'))
                response = bytearray()
                while len(response) < len(MAGIC) + 32:
                    chunk = connection.recv(len(MAGIC) + 32 - len(response))
                    if not chunk:
                        return False
                    response.extend(chunk)
                expected = MAGIC + hmac.digest(key, MAGIC + b'\0gameserver', 'sha256')
                return hmac.compare_digest(response, expected)
        except OSError:
            return False

    def check_alive(self):
        if self.process is None:
            raise RuntimeError('The local game logic process has not started')
        code = self.process.poll()
        if code is not None:
            raise RuntimeError(f'The local game logic process exited (code {code})')
        if self.admin_process is not None and self.admin_process.poll() is not None:
            raise RuntimeError('The local administration process exited')

    def _admin_ready(self):
        try:
            with self._opener.open(f'http://127.0.0.1:{self.admin_address[1]}/api/status', timeout=.25) as response:
                return response.status == 200 and json.load(response).get('admin_ready') is True
        except (OSError, ValueError):
            return False

    def start(self):
        if (self.process is not None and self.process.poll() is None
                and self.admin_process is not None and self.admin_process.poll() is None):
            return self
        self.close()
        for path in (self.reference_dir / 'bin' / 'gameserver.exe',
                     self.reference_dir / 'bin' / 'launcher.exe',
                     self.reference_dir / 'server_data' / 'static.sqlite3'):
            if not path.is_file():
                raise RuntimeError(f'Required local backend file is missing: {path.name}')
        self.data_dir.mkdir(parents=True, exist_ok=True)
        environment = self._child_environment()
        if self.character_dir is not None:
            if not (self.reference_dir / 'bin' / 'import-character.exe').is_file():
                raise RuntimeError('Required local backend file is missing: import-character.exe')
            self._import_character(environment)
        self.psk = secrets.token_hex(32)
        environment['RF4_GAME_TUNNEL_PSK'] = self.psk
        game_port, observation_port = self._port(), self._port()
        while observation_port == game_port:
            observation_port = self._port()
        self.address = ('127.0.0.1', game_port)
        admin_port = self._port()
        while admin_port in (game_port, observation_port):
            admin_port = self._port()
        self.admin_address = ('127.0.0.1', admin_port)
        self._temporary = tempfile.TemporaryDirectory(prefix='rf4-gameplay-')
        config_path = Path(self._temporary.name) / 'config.json'
        config = {
            'env': 'dev', 'log': {'level': 'warn', 'format': 'json'},
            'sqlite': {'path': str(self.database),
                       'static_path': str(self.reference_dir / 'server_data' / 'static.sqlite3')},
            'admin': {'username': 'admin', 'password': os.environ.get('RF4_LOCAL_ADMIN_PASSWORD', 'admin123'),
                      'jwt_secret': secrets.token_hex(32)},
            'game': {'tunnel': {'addr': f'127.0.0.1:{game_port}'},
                     'obs_addr': f'127.0.0.1:{observation_port}',
                     'keepalive_interval_sec': 5.0},
        }
        try:
            config_path.write_text(json.dumps(config, ensure_ascii=False), encoding='utf-8')
            self.process = subprocess.Popen(
                [str(self.reference_dir / 'bin' / 'gameserver.exe'), '-config', str(config_path),
                 '-parent-stdin'], cwd=self.reference_dir, env=environment,
                stdin=subprocess.PIPE, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
            deadline = time.monotonic() + self.startup_timeout
            while time.monotonic() < deadline:
                self.check_alive()
                if self._ready(observation_port) and self._verify_tunnel():
                    if self.admin_process is None:
                        self.admin_process = subprocess.Popen(
                            [str(self.reference_dir / 'bin' / 'launcher.exe'), '-config', str(config_path),
                             '-managed-admin', '-panel-addr', f'127.0.0.1:{admin_port}'],
                            cwd=self.reference_dir, env=environment, stdin=subprocess.PIPE,
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                            creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
                    if self._admin_ready():
                        self.check_alive()
                        return self
                time.sleep(.1)
            raise RuntimeError('The local game logic process did not become ready')
        except BaseException:
            self.close()
            raise

    def close(self):
        for process in (self.admin_process, self.process):
            self._stop_owned(process)
        self.admin_process = None
        self.process = None
        self.address = None
        self.admin_address = None
        self.psk = None
        if self._temporary is not None:
            self._temporary.cleanup()
            self._temporary = None

    def _stop_owned(self, process):
        if process is not None:
            if process.stdin is not None and not process.stdin.closed:
                try:
                    process.stdin.close()
                except OSError:
                    pass
            if process.poll() is None:
                try:
                    process.wait(timeout=self.shutdown_timeout)
                except subprocess.TimeoutExpired:
                    process.terminate()
                    try:
                        process.wait(timeout=2)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait(timeout=2)

    def __enter__(self):
        return self.start()

    def __exit__(self, *_):
        self.close()
