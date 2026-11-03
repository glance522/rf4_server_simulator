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
        if decode(self.receive()) != Message(1, 0, 1, TypedArray('524', []), True):
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
                           ('123123', b'salt', b'digest'))
                db.execute('INSERT INTO saves VALUES(?,?,?)', ('123123', '{"existing":true}', 1))
            pose = Record309(1., 2., 3., 4., 5., 6., 7., 8, 9)
            self.assertTrue(server.save_native_state(path, '123123', [pose, 10]))
            self.assertFalse(server.save_native_state(path, '123123', [pose, True]))
            self.assertFalse(server.save_native_state(path, '123123', [
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
            cls.http.character_profiles = {'123123': value}
            cls.http.character_user_ids = {'123123': 7357}
            cls.http.character_inventory = {'123123': {0: (value, ids)}}
            cls.http.character_details = {'123123': {(0, tuple(ids)): value}}
            cls.http.character_bootstrap = {'123123': {operation: value for operation in server.BOOTSTRAP_TYPES}}
            cls.http.character_mailbox = {'123123': {(0, 50): GuidArray([])}}
        cls.game = server.GameServer(('127.0.0.1', 0), server.GameHandler)
        cls.game.http = cls.http
        cls.http.game_port = cls.game.server_address[1]
        cls.threads = [threading.Thread(target=x.serve_forever, daemon=True)
                       for x in (cls.http, cls.game)]
        for thread in cls.threads:
            thread.start()
        status, _ = cls.http_request('POST', '/api/accounts/register',
                                     {'username': '123123', 'password': '123123'})
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
    def login(cls, password='123123'):
        query = urlencode({'login': '123123', 'password': hashlib.sha1(password.encode()).hexdigest(),
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
        self.assertEqual(int(resumed.findtext('userid')), self.http.character_user_ids['123123'])
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
            self.assertEqual(int(response.findtext('userid')), self.http.character_user_ids['123123'])
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
            self.assertEqual(client.call(12, (3, 1)), self.http.character_profiles['123123'].data)
            for request_id, (operation, value) in enumerate(
                    self.http.character_bootstrap['123123'].items(), 20):
                self.assertEqual(client.call(request_id, operation), value.data)
            inventory = self.http.character_inventory['123123']
            ids = inventory[0][1]
            self.assertTrue(ids)
            self.assertEqual(decode(client.call(40, (4, 4), [0, 0, 500])), GuidArray(ids))
            self.assertEqual(decode(client.call(41, (4, 4), [0, len(ids), 500])), GuidArray([]))
            details = self.http.character_details['123123'][0, tuple(ids)]
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
                                          ('123123',)).fetchone()[0])
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
