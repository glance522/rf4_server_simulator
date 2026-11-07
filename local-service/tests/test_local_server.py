"""Isolated regression tests; no remote servers or game input are used."""
from collections import deque
from contextlib import closing, contextmanager
import hashlib
import http.client
import json
from pathlib import Path
import secrets
import socket
import sqlite3
import struct
import sys
import tempfile
import threading
import time
import unittest
from urllib.parse import urlencode
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
import local_server as server
from rf4_phoenix import (
    Failure, GuidArray, Int64, Message, NativeValue, Record309, TypedArray, decode, encode,
)
from rf4_wire import Framer, SessionCipher, acknowledgement, data_frame


class Client:
    def __init__(self, port, token, resume=False):
        self.socket = socket.create_connection(('127.0.0.1', port), timeout=2)
        key = token.encode()
        self.socket.sendall(struct.pack('<BBI', 1, int(resume), len(key)) + key)
        self.accepted = self.read_exact(36) == server.HERMES_ACCEPT
        self.framer = Framer()
        self.incoming = SessionCipher(key)
        self.outgoing = SessionCipher(key)
        self.sequence = 1
        self.received_sequence = 0
        self.pending = deque()
        self.pushes = deque()
        if self.accepted:
            xml = ('<hermes><serializer>phoenix</serializer><compress>false</compress>'
                   f'<token>{token}</token></hermes>').encode()
            self.socket.sendall(data_frame(1, xml))
            self.assert_ready()

    def read_exact(self, count):
        data = bytearray()
        while len(data) < count:
            part = self.socket.recv(count - len(data))
            if not part:
                raise EOFError('Connection closed')
            data.extend(part)
        return bytes(data)

    def receive(self, timeout=2):
        deadline = time.monotonic() + timeout
        while not self.pending:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError('No RPC before deadline')
            self.socket.settimeout(remaining)
            part = self.socket.recv(65536)
            if not part:
                raise EOFError('Connection closed')
            for frame in self.framer.feed(part):
                if frame.kind == 0:
                    if frame.sequence != self.received_sequence + 1:
                        raise AssertionError('Server sequence is not continuous')
                    self.received_sequence = frame.sequence
                    self.socket.sendall(acknowledgement(frame.sequence))
                    self.pending.append(self.incoming.apply(frame.payload))
        return self.pending.popleft()

    def assert_ready(self):
        message = decode(self.receive())
        if (not isinstance(message, Message) or message.request_id <= 0
                or (message.code, message.subcode) != (0, 1)
                or message.arguments != TypedArray('524', []) or not message.flag):
            raise AssertionError('Invalid boot message')

    def send(self, message):
        self.sequence += 1
        self.socket.sendall(data_frame(self.sequence, self.outgoing.apply(encode(message))))

    def call(self, request_id, operation, values=()):
        self.send(Message(request_id, *operation, TypedArray('524', list(values)), False))
        deadline = time.monotonic() + 5
        while True:
            raw = self.receive(timeout=max(.01, deadline-time.monotonic()))
            if raw[:5] != b'\x01\xff\xff\xff\xff':
                break
            self.pushes.append(raw)
            if time.monotonic() >= deadline:
                raise TimeoutError('No response before deadline')
        if raw[:5] != b'\x01\xfe\xff\xff\xff':
            return decode(raw)
        if struct.unpack_from('<i', raw, 5)[0] != request_id:
            raise AssertionError('Wrong request identifier')
        return raw[9:]

    def close(self):
        self.socket.close()


class CodecTests(unittest.TestCase):
    def test_rc4_known_vector_and_stream_continuity(self):
        cipher = SessionCipher(b'Key')
        encrypted = cipher.apply(b'Plain') + cipher.apply(b'text')
        self.assertEqual(encrypted.hex(), 'bbf316e8d940af0ad3')
        self.assertEqual(SessionCipher(b'Key').apply(encrypted), b'Plaintext')

    def test_zero_frames_fragmentation_and_invalid_length(self):
        framer = Framer()
        stream = b'\0' * 4 + data_frame(1, b'abc') + b'\0' * 8 + acknowledgement(1)
        frames = []
        for byte in stream:
            frames.extend(framer.feed(bytes([byte])))
        self.assertEqual([(x.kind, x.sequence, x.payload) for x in frames],
                         [(0, 1, b'abc'), (1, 1, b'')])
        self.assertFalse(framer.buffer)
        with self.assertRaises(ValueError):
            Framer().feed(b'\xff' * 4)

    def test_native_pose_fixture_and_truncation(self):
        # Native descriptor 309 writes seven Singles followed by two Int32s.
        fixture = bytes.fromhex(
            '01350100000000803f0000004000004040000080400000a0400000c0400000e0400800000009000000')
        value = Record309(1., 2., 3., 4., 5., 6., 7., 8, 9)
        self.assertEqual(decode(fixture), value)
        self.assertEqual(encode(value), fixture)
        for cut in range(len(fixture)):
            with self.subTest(cut=cut), self.assertRaises(ValueError):
                decode(fixture[:cut])
        with self.assertRaises(ValueError):
            decode(fixture + b'\0')


class MigrationTests(unittest.TestCase):
    def test_existing_database_upgrade_isolated_and_repeatable(self):
        with tempfile.TemporaryDirectory(prefix='rf4-migration-') as temporary:
            path = Path(temporary) / 'local.sqlite3'
            source = ROOT / 'data' / 'local.sqlite3'
            if source.is_file():
                with closing(sqlite3.connect(source.as_uri() + '?mode=ro', uri=True)) as original:
                    with closing(sqlite3.connect(path)) as copied:
                        original.backup(copied)
            else:
                with closing(sqlite3.connect(path)) as copied:
                    copied.executescript('''CREATE TABLE accounts(username TEXT PRIMARY KEY COLLATE NOCASE,
                        salt BLOB NOT NULL,digest BLOB NOT NULL,native_digest BLOB);
                        CREATE TABLE sessions(digest TEXT PRIMARY KEY,username TEXT NOT NULL REFERENCES accounts(username),expires INTEGER NOT NULL);
                        CREATE TABLE saves(username TEXT PRIMARY KEY REFERENCES accounts(username),data TEXT NOT NULL,updated INTEGER NOT NULL);
                        INSERT INTO accounts VALUES('fixture',X'01',X'02',NULL);
                        INSERT INTO saves VALUES('fixture','{"preserved":true}',1);
                        PRAGMA user_version=2;''')
            with closing(sqlite3.connect(path)) as copied:
                before = {table: copied.execute(f'SELECT * FROM {table} ORDER BY 1').fetchall()
                          for table in ('accounts', 'saves')}
            server.initialize(path)
            server.initialize(path)
            with closing(sqlite3.connect(path)) as copied:
                self.assertEqual(copied.execute('PRAGMA user_version').fetchone()[0], 3)
                for table, rows in before.items():
                    self.assertEqual(copied.execute(f'SELECT * FROM {table} ORDER BY 1').fetchall(), rows)
                self.assertFalse(copied.execute('PRAGMA foreign_key_check').fetchall())
                self.assertEqual(copied.execute('PRAGMA integrity_check').fetchone()[0], 'ok')

    def test_pose_persists_separately_from_test_save(self):
        with tempfile.TemporaryDirectory(prefix='rf4-state-') as temporary:
            path = Path(temporary) / 'local.sqlite3'
            server.initialize(path)
            with server.connect(path) as db:
                db.execute('INSERT INTO accounts(username,salt,digest) VALUES(?,?,?)',
                           ('local_user', b'salt', b'digest'))
                db.execute('INSERT INTO saves VALUES(?,?,?)', ('local_user', '{"existing":true}', 1))
            pose = Record309(1., 2., 3., 4., 5., 6., 7., 8, 9)
            self.assertTrue(server.save_native_state(path, 'local_user', [pose, 10]))
            self.assertFalse(server.save_native_state(path, 'local_user', [pose, True]))
            self.assertFalse(server.save_native_state(path, 'local_user', [
                Record309(float('nan'), 2., 3., 4., 5., 6., 7., 8, 9), 10]))
            server.initialize(path)
            with server.connect(path) as db:
                state = json.loads(db.execute('SELECT data FROM native_states').fetchone()[0])
                self.assertEqual((state['f1'], state['i2'], state['context']), (1., 9, 10))
                self.assertEqual(db.execute('SELECT data FROM saves').fetchone()[0], '{"existing":true}')


class LocalProtocolTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory(prefix='rf4-protocol-')
        cls.http = server.Server(('127.0.0.1', 0), server.Handler)
        cls.http.db_path = Path(cls.temporary.name) / 'local.sqlite3'
        server.initialize(cls.http.db_path)
        cls.http.control = secrets.token_hex(16)
        cls.http.client_events = deque(maxlen=100)
        cls.http.events_lock = threading.Lock()
        cls.http.catalog = server.load_catalog(ROOT / 'data' / 'catalog')
        characters, source_ids = ROOT / 'data' / 'characters', {}
        cls.http.character_inventory = server.load_character_inventory(characters, source_ids)
        cls.http.character_details = server.load_character_details(characters, cls.http.character_inventory)
        cls.http.character_profiles = server.load_character_profiles(characters, source_ids)
        cls.http.character_user_ids = source_ids
        cls.http.character_bootstrap = server.load_character_bootstrap(characters, cls.http.character_profiles)
        cls.http.character_mailbox = server.load_character_mailbox(characters, cls.http.character_profiles)
        if not cls.http.character_profiles:
            from uuid import UUID
            ids = [UUID('00000000-0000-0000-0000-000000000001')]
            value = NativeValue(encode(TypedArray('524', [])))
            cls.http.character_profiles = {'local_user': value}
            cls.http.character_user_ids = {'local_user': 4242}
            cls.http.character_inventory = {'local_user': {0: (value, ids)}}
            cls.http.character_details = {'local_user': {(0, tuple(ids)): value}}
            cls.http.character_bootstrap = {'local_user': {operation: value for operation in server.BOOTSTRAP_TYPES}}
            cls.http.character_mailbox = {'local_user': {(0, 50): GuidArray([])}}
        cls.game = server.GameServer(('127.0.0.1', 0), server.GameHandler)
        cls.game.http = cls.http
        cls.http.game_port = cls.game.server_address[1]
        cls.threads = [threading.Thread(target=x.serve_forever, daemon=True)
                       for x in (cls.http, cls.game)]
        for thread in cls.threads:
            thread.start()
        status, _ = cls.http_request('POST', '/api/accounts/register',
                                     {'username': 'local_user', 'password': 'local_user'})
        if status != 201:
            raise AssertionError('Isolated account registration failed')

    @classmethod
    def tearDownClass(cls):
        for instance in (cls.http, cls.game):
            instance.shutdown()
            instance.server_close()
        for thread in cls.threads:
            thread.join(timeout=2)
        cls.temporary.cleanup()

    @classmethod
    def http_request(cls, method, path, data=None):
        connection = http.client.HTTPConnection('127.0.0.1', cls.http.server_port, timeout=3)
        try:
            body = json.dumps(data).encode() if data is not None else None
            headers = {'Content-Type': 'application/json'} if body is not None else {}
            connection.request(method, path, body=body, headers=headers)
            response = connection.getresponse()
            return response.status, response.read()
        finally:
            connection.close()

    def test_client_events_exposes_only_protocol_metadata(self):
        server.record_forwarded(self.http, 'backend-to-client',
                                encode(Message(20, 14, 14, TypedArray('524', ['private-value']), True)))
        server.record_forwarded(self.http, 'backend-to-client',
                                encode(Failure(21, 'private-error', 'private-message')))
        status, raw = self.http_request('GET', '/api/client-events')
        self.assertEqual(status, 200)
        report = json.loads(raw)
        self.assertFalse(report['valuesRecorded'])
        self.assertFalse(report['protocol']['valuesRecorded'])
        self.assertGreaterEqual(report['protocol']['counts']['backend-to-client:message:14/14'], 1)
        self.assertEqual(report['protocol']['recentFishing'][-1]['requestID'], 20)
        self.assertEqual(report['protocol']['recentResults'][-1]['kind'], 'failure')
        self.assertNotIn(b'private', raw)

    @classmethod
    def login(cls, password='local_user'):
        query = urlencode({'login': 'local_user', 'password': hashlib.sha1(password.encode()).hexdigest(),
                           'version': '4.0.25034'})
        status, raw = cls.http_request('GET', '/lv58/https://api.rf4game.ru/login.php?' + query)
        if status != 200:
            raise AssertionError('Local login HTTP failure')
        return ET.fromstring(raw)

    @contextmanager
    def client(self):
        response = self.login()
        self.assertEqual(response.get('success'), 'true')
        client = Client(self.http.game_port, response.findtext('token'))
        self.assertTrue(client.accepted)
        try:
            yield client
        finally:
            client.close()

    def test_remembered_login_uses_valid_local_token_and_trusted_identity(self):
        first = self.login()
        saved_token = first.findtext('logintoken')
        # 初始化不应清除仍有效的已记住凭据。
        server.initialize(self.http.db_path)
        query = urlencode({'token': saved_token, 'id': 'untrusted-client-id', 'version': '4.0.25034'})
        status, raw = self.http_request('GET', '/lv58/https://api.rf4game.ru/login_token.php?' + query)
        self.assertEqual(status, 200)
        resumed = ET.fromstring(raw)
        self.assertEqual(resumed.get('success'), 'true')
        self.assertEqual(int(resumed.findtext('userid')), self.http.character_user_ids['local_user'])
        self.assertNotEqual(resumed.findtext('token'), first.findtext('token'))
        client = Client(self.http.game_port, resumed.findtext('token'))
        try:
            self.assertTrue(client.accepted)
        finally:
            client.close()
        _, metadata = self.http_request('GET', '/api/client-events')
        self.assertNotIn(saved_token.encode(), metadata)
        self.assertNotIn(resumed.findtext('token').encode(), metadata)

    def test_remembered_login_rejects_expired_missing_and_unknown_tokens_as_xml(self):
        saved_token = self.login().findtext('logintoken')
        with server.connect(self.http.db_path) as db:
            db.execute('UPDATE sessions SET expires=? WHERE digest=?',
                       (int(time.time()) - 1, hashlib.sha256(saved_token.encode()).hexdigest()))
            db.commit()
        for values in ({'token': saved_token, 'version': '4.0.25034'},
                       {'token': '0' * 64, 'version': '4.0.25034'},
                       {'token': 'bad', 'version': '4.0.25034'},
                       {'version': '4.0.25034'},
                       {'token': saved_token, 'version': 'unsupported'}):
            status, raw = self.http_request('GET', '/lv58/https://api.rf4game.ru/login_token.php?' + urlencode(values))
            self.assertEqual(status, 200)
            self.assertEqual(ET.fromstring(raw).get('success'), 'false')

    def test_authentication_and_resume_rejection(self):
        self.assertEqual(self.login('wrong-password').get('success'), 'false')
        with self.client():
            response = self.login()
            self.assertEqual(int(response.findtext('userid')), self.http.character_user_ids['local_user'])
            client = Client(self.http.game_port, response.findtext('token'), resume=True)
            try:
                self.assertFalse(client.accepted)
            finally:
                client.close()
        invalid = Client(self.http.game_port, 'invalid-local-test-token')
        try:
            self.assertFalse(invalid.accepted)
        finally:
            invalid.close()

    def test_bootstrap_inventory_and_mailbox(self):
        with self.client() as client:
            self.assertEqual(decode(client.call(10, (1, 1), ['4.0.25034'])), 'LocalServer/4.0.25034')
            self.assertIsInstance(decode(client.call(11, (1, 2))), Int64)
            self.assertEqual(client.call(12, (3, 1)), self.http.character_profiles['local_user'].data)
            for request_id, (operation, value) in enumerate(
                    self.http.character_bootstrap['local_user'].items(), 20):
                self.assertEqual(client.call(request_id, operation), value.data)
            inventory = self.http.character_inventory['local_user']
            ids = inventory[0][1]
            self.assertTrue(ids)
            self.assertEqual(decode(client.call(40, (4, 4), [0, 0, 500])), GuidArray(ids))
            self.assertEqual(decode(client.call(41, (4, 4), [0, len(ids), 500])), GuidArray([]))
            details = self.http.character_details['local_user'][0, tuple(ids)]
            self.assertEqual(client.call(42, (4, 5), [0, GuidArray(ids)]), details.data)
            self.assertEqual(decode(client.call(43, (19, 6), [0, 50])), GuidArray([]))
            self.assertEqual(decode(client.call(44, (1, 18),
                ['CN', False, False, 1, 2, 'local', 'test', 'version'])), True)

    def test_unimplemented_response_preserves_same_session(self):
        with self.client() as client:
            for request_id, operation, values in (
                    (50, (17, 7), []), (51, (11, 2), [1]), (52, (3, 18), []),
                    (53, (4, 4), [0, -1, 500]), (54, (19, 6), [0, 51])):
                with self.subTest(operation=operation):
                    result = client.call(request_id, operation, values)
                    self.assertIsInstance(result, Failure)
                    self.assertEqual(result.request_id, request_id)
            self.assertIsInstance(decode(client.call(55, (1, 2))), Int64)

    def test_idle_keepalive_does_not_advance_cipher_or_sequence(self):
        with self.client() as client:
            before = (client.incoming.index, client.incoming.other, list(client.incoming.state))
            client.socket.settimeout(server.TRANSPORT_KEEPALIVE_SECONDS + 2)
            for _ in range(2):
                self.assertEqual(client.read_exact(4), b'\0' * 4)
            self.assertEqual(client.received_sequence, 1)
            self.assertEqual((client.incoming.index, client.incoming.other, client.incoming.state), before)
            self.assertIsInstance(decode(client.call(56, (1, 2))), Int64)
            self.assertEqual(client.received_sequence, 2)

    def test_oneway_pose_saved_without_rpc_reply(self):
        with self.client() as client:
            pose = Record309(1., 2., 3., 4., 5., 6., 7., 8, 9)
            client.send(Message(60, 3, 9, TypedArray('524', [pose, 10]), True))
            with self.assertRaises(TimeoutError):
                client.receive(timeout=.15)
            self.assertIsInstance(decode(client.call(61, (1, 2))), Int64)
        with server.connect(self.http.db_path) as db:
            state = json.loads(db.execute('SELECT data FROM native_states WHERE username=?',
                                          ('local_user',)).fetchone()[0])
        self.assertEqual((state['f7'], state['i1'], state['context']), (7., 8, 10))

    def test_unknown_parameter_type_has_feedback_and_keeps_stream(self):
        unknown = NativeValue(b'\x01' + struct.pack('<i', 2147483647))
        with self.client() as client:
            result = client.call(70, (14, 2), [unknown])
            self.assertIsInstance(result, Failure)
            self.assertEqual(result.request_id, 70)
            client.send(Message(71, 14, 7, TypedArray('524', [unknown]), True))
            with self.assertRaises(TimeoutError):
                client.receive(timeout=.15)
            self.assertIsInstance(decode(client.call(72, (1, 2))), Int64)


if __name__ == '__main__':
    unittest.main(verbosity=2)


class CharacterIdentityTests(unittest.TestCase):
    def test_existing_import_metadata_preserves_binding_without_snapshots(self):
        with tempfile.TemporaryDirectory(prefix='rf4-identity-') as temporary:
            path = Path(temporary) / 'gameplay.sqlite3'
            with closing(sqlite3.connect(path)) as db:
                db.execute('CREATE TABLE kv(k TEXT PRIMARY KEY,v TEXT)')
                record = {'schema': 2, 'clientVersion': '4.0.25034',
                          'targetAccount': 'Legacy_User', 'sourceUserID': 4242}
                db.execute('INSERT INTO kv VALUES(?,?)',
                           ('native-import:4242:inventory', json.dumps(record)))
                db.commit()
            before = path.read_bytes()
            identities = server.load_character_user_ids(path, {})
            self.assertEqual(identities, {'legacy_user': 4242})
            self.assertEqual(path.read_bytes(), before)
            from types import SimpleNamespace
            http = SimpleNamespace(character_user_ids=identities)
            self.assertEqual(server.local_game_uid(http, 'LEGACY_USER', 1), 4242)
            self.assertIsNone(server.local_game_uid(http, 'new_user', 4242))
            self.assertEqual(server.local_game_uid(http, 'new_user', 2), 2)
            with self.assertRaises(RuntimeError):
                server.load_character_user_ids(path, {'legacy_user': 4243})
            with self.assertRaises(RuntimeError):
                server.load_character_user_ids(path, {'another_user': 4242})

    def test_snapshot_directories_are_bound_to_their_own_accounts(self):
        with tempfile.TemporaryDirectory(prefix='rf4-identities-') as temporary:
            root = Path(temporary)
            kinds = [str(kind) for kind in list(range(12)) + [14, 17, 18, 19]]
            for account, uid in [('first_user', 4242), ('second_user', 4243)]:
                directory = root / account
                directory.mkdir()
                record = {'schema': 2, 'clientVersion': '4.0.25034', 'targetAccount': account,
                          'sourceCharacter': 'redacted', 'sourceUserID': uid,
                          'countsByKind': {kind: {'a': 0, 'b': 0, 'c': 0} for kind in kinds},
                          'idsByKind': {kind: [] for kind in kinds}}

                (directory / 'inventory.json').write_text(json.dumps(record), encoding='utf-8')
                common = {'schema': 1, 'clientVersion': '4.0.25034',
                          'targetAccount': account, 'namesRedacted': True}
                profile = b'\x01' + struct.pack('<iI', 307, uid)
                digest = hashlib.sha256(profile).hexdigest()
                name = f'profile-{digest}.bin'
                (directory / name).write_bytes(profile)
                (directory / 'profile.json').write_text(json.dumps(dict(common, operation=[3, 1],
                    typeID=307, file=name, sha256=digest, size=len(profile))), encoding='utf-8')
                (directory / 'details.json').write_text(json.dumps(dict(common, entries=[])), encoding='utf-8')
                bootstrap = directory / 'bootstrap'
                bootstrap.mkdir()
                entries = []
                for operation, type_id in server.BOOTSTRAP_TYPES.items():
                    if operation == (6, 2):
                        value = encode(None)
                    elif operation == (24, 12) or operation in server.BOOTSTRAP_ARRAYS:
                        value = encode(TypedArray('524', []))
                    else:
                        value = b'\x01' + struct.pack('<i', type_id)
                    digest = hashlib.sha256(value).hexdigest()
                    name = f'{operation[0]}-{operation[1]}-{digest}.bin'
                    (bootstrap / name).write_bytes(value)
                    entries.append({'operation': list(operation), 'typeID': type_id,
                                    'file': name, 'sha256': digest, 'size': len(value)})
                (bootstrap / 'index.json').write_text(json.dumps(dict(common, entries=entries)), encoding='utf-8')
                (directory / 'mailbox.json').write_text(json.dumps({
                    'schema': 1, 'clientVersion': '4.0.25034', 'targetAccount': account,
                    'operation': [19, 6], 'query': [0, 50], 'ids': []}), encoding='utf-8')
            identities = {}
            values = server.load_character_inventory(root, identities)
            self.assertEqual(set(values), {'first_user', 'second_user'})
            self.assertEqual(identities, {'first_user': 4242, 'second_user': 4243})
            profiles = server.load_character_profiles(root, identities)
            for loaded in (server.load_character_details(root, values), profiles,
                           server.load_character_bootstrap(root, profiles),
                           server.load_character_mailbox(root, profiles)):
                self.assertEqual(set(loaded), {'first_user', 'second_user'})
            record['targetAccount'] = 'first_user'
            (root / 'second_user' / 'inventory.json').write_text(json.dumps(record), encoding='utf-8')
            with self.assertRaises(RuntimeError):
                server.load_character_inventory(root, {})
