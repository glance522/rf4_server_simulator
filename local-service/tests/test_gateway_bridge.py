"""Gateway-to-tunnel boundary tests; the business peer is isolated."""
from collections import deque
from contextlib import contextmanager
import hashlib
import json
from pathlib import Path
import struct
import tempfile
import threading
from types import SimpleNamespace
import unittest
from uuid import UUID

from test_backend import peer, PSK, read_record, _record, OPEN, DATA, CLOSE
from test_local_server import Client, server, Message, TypedArray, encode
from rf4_phoenix import NativeValue, Result, Failure


@contextmanager
def gateway(address, psk=PSK, diagnostics=None, user_id=4242):
    with tempfile.TemporaryDirectory(prefix='rf4-gateway-') as temporary:
        path = Path(temporary) / 'local.sqlite3'
        server.initialize(path)
        token = 'isolated-authenticated-client-token'
        with server.connect(path) as db:
            db.execute('INSERT INTO accounts(username,salt,digest) VALUES(?,?,?)',
                       ('local_user', b'test-salt', b'test-digest'))
            db.execute('INSERT INTO sessions VALUES(?,?,?)',
                       (hashlib.sha256(token.encode()).hexdigest(), 'local_user', 2**31))
        http = SimpleNamespace(db_path=path, backend_config=(address, psk),
                               character_user_ids={'local_user': user_id},
                               events_lock=threading.Lock(), client_events=deque(maxlen=100),
                               protocol_diagnostics=diagnostics)
        game = server.GameServer(('127.0.0.1', 0), server.GameHandler)
        game.http = http
        thread = threading.Thread(target=game.serve_forever, daemon=True)
        thread.start()
        try:
            yield game.server_address[1], token
        finally:
            game.shutdown()
            game.server_close()
            thread.join(timeout=2)


class GatewayBridgeTests(unittest.TestCase):
    def test_backend_boot_opaque_rpc_push_and_close(self):
        native = NativeValue(b'\x01' + struct.pack('<i', 158) + UUID(int=10).bytes_le
                             + struct.pack('<II', 0x3f800000, 0x40000000) + UUID(int=20).bytes_le)
        request = Message(80, 250, 250, TypedArray('524', [native]), False)
        response = encode(Result(80, native))
        push = encode(Message(2, 2, 2, TypedArray('524', [123]), True))
        completed = threading.Event()

        def action(connection):
            kind, sid, payload = read_record(connection)
            self.assertEqual(kind, OPEN)
            identity = json.loads(payload)
            self.assertEqual(identity['token'].split('|')[2], '4242')
            self.assertNotIn('isolated-authenticated-client-token', identity['token'])
            self.assertFalse(identity['reconnect'])
            boot = _record(DATA, sid, encode(Message(1, 0, 1, TypedArray('524', []), True)))
            connection.sendall(boot[:7])
            connection.sendall(boot[7:])
            self.assertEqual(read_record(connection), (DATA, sid, encode(request)))
            connection.sendall(_record(DATA, sid, response) + _record(DATA, sid, push))
            self.assertEqual(read_record(connection), (CLOSE, sid, b'client_closed'))
            completed.set()

        with peer(action) as address, gateway(address) as (port, token):
            client = Client(port, token)
            try:
                client.send(request)
                self.assertEqual(client.receive(), response)
                self.assertEqual(client.receive(), push)
                self.assertEqual(client.received_sequence, 3)
            finally:
                client.close()
            self.assertTrue(completed.wait(timeout=2))


    def test_bite_push_metadata_survives_movement_without_decoding_values(self):
        # The bridge must not need the unknown native type 27 descriptor.
        secret = b'private-fish-payload'
        fish = NativeValue(b'\x01' + struct.pack('<i', 27) + secret)
        bite = encode(Message(2, 14, 14, TypedArray('524', [UUID(int=10), fish]), True))
        success = encode(Result(900, fish))
        failure = encode(Failure(901, 'private-error-type', 'private-error-text'))
        diagnostics = server.ProtocolDiagnostics()
        completed = threading.Event()

        def action(connection):
            kind, sid, _ = read_record(connection)
            self.assertEqual(kind, OPEN)
            connection.sendall(_record(DATA, sid, encode(Message(1, 0, 1, TypedArray('524', []), True))))
            connection.sendall(_record(DATA, sid, bite))
            for number in range(500):
                kind, received_sid, payload = read_record(connection)
                self.assertEqual((kind, received_sid), (DATA, sid))
                self.assertEqual(payload, encode(Message(1000 + number, 24, 1,
                                                       TypedArray('524', []), True)))
            connection.sendall(_record(DATA, sid, success) + _record(DATA, sid, failure))
            self.assertEqual(read_record(connection), (CLOSE, sid, b'client_closed'))
            completed.set()

        with peer(action) as address, gateway(address, diagnostics=diagnostics) as (port, token):
            client = Client(port, token)
            try:
                self.assertEqual(client.receive(), bite)
                for number in range(500):
                    client.send(Message(1000 + number, 24, 1, TypedArray('524', []), True))
                self.assertEqual(client.receive(), success)
                self.assertEqual(client.receive(), failure)
            finally:
                client.close()
            self.assertTrue(completed.wait(timeout=2))
        report = diagnostics.snapshot()
        self.assertEqual(report['counts']['backend-to-client:message:14/14'], 1)
        self.assertEqual(report['counts']['client-to-backend:message:24/1'], 500)
        self.assertEqual(report['counts']['backend-to-client:result'], 1)
        self.assertEqual(report['counts']['backend-to-client:failure'], 1)
        self.assertEqual(len(report['recentFishing']), 1)
        event = report['recentFishing'][0]
        self.assertEqual((event['direction'], event['code'], event['subcode']),
                         ('backend-to-client', 14, 14))
        self.assertTrue(event['forwarded'])
        self.assertEqual(event['bytes'], len(bite))
        self.assertNotIn('private', json.dumps(report))

    def test_cafe_scene_diagnostics_survive_heartbeats_without_credentials(self):
        diagnostics = server.ProtocolDiagnostics()
        for number in range(20):
            diagnostics.record('client-to-backend', encode(Message(number, 20, 3,
                               TypedArray('524', ['fmarket_02', 'private-value']), False)))
        for number in range(200):
            diagnostics.record('client-to-backend', encode(Message(number, 24, 1,
                               TypedArray('524', []), False)))
        report = diagnostics.snapshot()
        self.assertEqual(len(report['recentCafe']), 16)
        self.assertEqual(report['recentCafe'][-1]['scene'], 'fmarket_02')
        self.assertEqual(report['recordedValueFields'], ['cafe.scene'])
        self.assertNotIn('private-value', json.dumps(report))

    def test_protocol_diagnostics_memory_is_bounded(self):
        diagnostics = server.ProtocolDiagnostics()
        for number in range(2000):
            payload = encode(Message(number, number // 256, number % 256,
                                     TypedArray('524', []), False))
            diagnostics.record('client-to-backend', payload)
            diagnostics.record('backend-to-client', encode(Result(number, None)))
        for number in range(100):
            diagnostics.record('backend-to-client', encode(Message(number, 14, 14,
                                                                   TypedArray('524', []), True)))
        report = diagnostics.snapshot()
        self.assertLessEqual(len(report['counts']), 513)
        self.assertEqual(len(report['recentFishing']), 64)
        self.assertEqual(len(report['recentResults']), 32)
        self.assertGreater(report['counts']['overflow'], 0)


if __name__ == '__main__':
    unittest.main(verbosity=2)
