"""Loopback tunnel regression tests without the game or a remote service."""
from contextlib import contextmanager
import hmac
import json
from pathlib import Path
import queue
import socket
import struct
import sys
import threading
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from rf4_backend import (
    BackendProtocolError, BackendSession, RecordFramer,
    MAGIC, MAX_PAYLOAD, OPEN, DATA, CLOSE, PING, PONG, _record,
)

PSK = 'isolated-local-tunnel-test-key-32-bytes'


def read_exact(connection, count):
    data = bytearray()
    while len(data) < count:
        chunk = connection.recv(count - len(data))
        if not chunk:
            raise EOFError()
        data.extend(chunk)
    return bytes(data)


def read_record(connection):
    length = struct.unpack('<I', read_exact(connection, 4))[0]
    kind, session_id = struct.unpack('<BQ', read_exact(connection, 9))
    return kind, session_id, read_exact(connection, length - 9)


@contextmanager
def peer(action, valid_key=True):
    listener = socket.socket()
    listener.bind(('127.0.0.1', 0))
    listener.listen(1)
    listener.settimeout(3)
    errors = queue.Queue()

    def serve():
        try:
            with listener.accept()[0] as connection:
                connection.settimeout(3)
                greeting = read_exact(connection, len(MAGIC) + 32)
                expected = MAGIC + hmac.digest(PSK.encode(), MAGIC + b'\0gateway', 'sha256')
                if not hmac.compare_digest(greeting, expected):
                    raise AssertionError('Unexpected gateway handshake')
                key = PSK if valid_key else PSK + '-wrong'
                connection.sendall(MAGIC + hmac.digest(key.encode(), MAGIC + b'\0gameserver', 'sha256'))
                action(connection)
        except BaseException as error:
            errors.put(error)

    thread = threading.Thread(target=serve, daemon=True)
    thread.start()
    try:
        yield listener.getsockname()
    finally:
        thread.join(timeout=4)
        listener.close()
        if thread.is_alive():
            raise AssertionError('Test backend thread did not exit')
        if not errors.empty():
            raise errors.get()


class FramerTests(unittest.TestCase):
    def test_fragmented_records_and_exact_payload(self):
        first = _record(DATA, 12, b'\x01\xfe\xff\xff\xff\0payload')
        second = _record(CLOSE, 12, b'finished')
        framer = RecordFramer()
        found = []
        for value in first + second:
            found.extend(framer.feed(bytes([value])))
        self.assertEqual(found, [(DATA, 12, b'\x01\xfe\xff\xff\xff\0payload'),
                                 (CLOSE, 12, b'finished')])
        self.assertFalse(framer.buffer)

    def test_invalid_lengths_and_types(self):
        for length in (0, 1, 8, MAX_PAYLOAD + 10, 2**32 - 1):
            with self.subTest(length=length), self.assertRaises(BackendProtocolError):
                RecordFramer().feed(struct.pack('<I', length))
        with self.assertRaises(BackendProtocolError):
            RecordFramer().feed(struct.pack('<IBQ', 9, 255, 1))


class BackendTests(unittest.TestCase):
    def test_auth_open_data_ping_close(self):
        recorded = queue.Queue()
        opaque = b'\x01\xff\xff\xff\xff\x08\0\0\0\x14\x02opaque'

        def action(connection):
            kind, session_id, payload = read_record(connection)
            recorded.put((kind, session_id, json.loads(payload)))
            self.assertEqual(read_record(connection), (DATA, session_id, opaque))
            data = _record(PING, 0, b'') + _record(DATA, session_id, opaque)
            connection.sendall(data[:5])
            connection.sendall(data[5:])
            self.assertEqual(read_record(connection), (PONG, 0, b''))
            self.assertEqual(read_record(connection), (CLOSE, session_id, b'client_closed'))

        with peer(action) as address:
            with BackendSession(address, PSK, 4242, 'external-token-stays-private') as session:
                kind, session_id, opened = recorded.get(timeout=2)
                self.assertEqual((kind, session_id), (OPEN, session.session_id))
                self.assertEqual(opened['token'].split('|')[2], '4242')
                self.assertNotIn('external-token-stays-private', opened['token'])
                self.assertFalse(opened['reconnect'])
                session.send_data(opaque)
                received = []
                while not received:
                    received.extend(session.receive())
                self.assertEqual(received, [(DATA, session.session_id, opaque)])
            self.assertEqual(session.socket.fileno(), -1)
            session.close()

    def test_internal_identity_stable_and_bound_to_user(self):
        opened = []

        def action(connection):
            _, _, payload = read_record(connection)
            opened.append(json.loads(payload)['token'])
            read_record(connection)

        identifiers = []
        for user_id in (4242, 4242, 7358):
            with peer(action) as address:
                with BackendSession(address, PSK, user_id, 'same-external-token') as session:
                    identifiers.append(session.session_id)
        self.assertEqual(opened[0], opened[1])
        self.assertNotEqual(opened[0], opened[2])
        self.assertEqual(len(set(identifiers)), 3)

    def test_remote_address_invalid_identity_or_weak_key_never_dials(self):
        cases = [(('192.0.2.10', 9401), PSK, 4242, 'token'),
                 (('127.0.0.1', 0), PSK, 4242, 'token'),
                 (('127.0.0.1', 9401), PSK, 0, 'token'),
                 (('127.0.0.1', 9401), PSK, True, 'token'),
                 (('127.0.0.1', 9401), PSK, 4242, ''),
                 (('127.0.0.1', 9401), 'weak', 4242, 'token')]
        with patch('rf4_backend.socket.create_connection') as dial:
            for values in cases:
                with self.subTest(values=values), self.assertRaises(ValueError):
                    BackendSession(*values)
            dial.assert_not_called()

    def test_wrong_psk_closes_without_open(self):
        def action(connection):
            self.assertEqual(connection.recv(1), b'')
        with peer(action, valid_key=False) as address:
            with self.assertRaises(BackendProtocolError):
                BackendSession(address, PSK, 4242, 'token')

    def test_wrong_session_id_is_rejected(self):
        def action(connection):
            _, session_id, _ = read_record(connection)
            connection.sendall(_record(DATA, session_id + 1, b'wrong-session'))
            read_record(connection)
        with peer(action) as address:
            with BackendSession(address, PSK, 4242, 'token') as session:
                with self.assertRaises(BackendProtocolError):
                    session.receive()

    def test_truncated_record_and_eof(self):
        def action(connection):
            read_record(connection)
            connection.sendall(_record(DATA, 1, b'cut')[:6])
        with peer(action) as address:
            with BackendSession(address, PSK, 4242, 'token') as session:
                self.assertEqual(session.receive(), [])
                with self.assertRaises(BackendProtocolError):
                    session.receive()

    def test_handshake_timeout_closes_the_socket(self):
        failures = queue.Queue()
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            listener.listen(1)
            listener.settimeout(2)

            def serve():
                try:
                    with listener.accept()[0] as connection:
                        connection.settimeout(2)
                        read_exact(connection, len(MAGIC) + 32)
                        self.assertEqual(connection.recv(1), b'')
                except BaseException as error:
                    failures.put(error)

            thread = threading.Thread(target=serve, daemon=True)
            thread.start()
            with self.assertRaises(TimeoutError):
                BackendSession(listener.getsockname(), PSK, 4242, 'token', timeout=.05)
            thread.join(timeout=2)
            self.assertFalse(thread.is_alive())
            if not failures.empty():
                raise failures.get()


if __name__ == '__main__':
    unittest.main(verbosity=2)
