"""Authenticated, loopback-only tunnel to the local game logic service."""
import hashlib
import hmac
import ipaddress
import json
import secrets
import socket
import struct
import threading

OPEN, DATA, CLOSE, PING, PONG = range(1, 6)
MAX_PAYLOAD = 32 * 1024 * 1024
MAGIC = b'RF4T1'
_sid_lock = threading.Lock()
_last_sid = secrets.randbits(63)


class BackendProtocolError(ValueError):
    pass


def _session_id():
    global _last_sid
    with _sid_lock:
        _last_sid = (_last_sid + 1) & (2**64 - 1)
        if _last_sid == 0:
            _last_sid = 1
        return _last_sid


def _record(kind, session_id, payload):
    if kind not in (OPEN, DATA, CLOSE, PING, PONG):
        raise BackendProtocolError('Unknown local backend record')
    if len(payload) > MAX_PAYLOAD:
        raise BackendProtocolError('Local backend record exceeds the limit')
    return struct.pack('<IBQ', 9 + len(payload), kind, session_id) + payload


class RecordFramer:
    def __init__(self):
        self.buffer = bytearray()

    def feed(self, chunk):
        self.buffer.extend(chunk)
        records = []
        while len(self.buffer) >= 4:
            length = struct.unpack_from('<I', self.buffer)[0]
            if not 9 <= length <= MAX_PAYLOAD + 9:
                raise BackendProtocolError('Invalid local backend record length')
            if len(self.buffer) < length + 4:
                break
            kind, session_id = struct.unpack_from('<BQ', self.buffer, 4)
            if kind not in (OPEN, DATA, CLOSE, PING, PONG):
                raise BackendProtocolError('Unknown local backend record')
            records.append((kind, session_id, bytes(self.buffer[13:length+4])))
            del self.buffer[:length+4]
        return records


class BackendSession:
    """One authenticated user per tunnel; the caller owns its single read loop."""
    def __init__(self, address, psk, user_id, client_token, timeout=5):
        host, port = address
        if (not ipaddress.ip_address(host).is_loopback or type(port) is not int
                or not 1 <= port <= 65535):
            raise ValueError('The game logic service must use a loopback address')
        if (type(user_id) is not int or not 0 < user_id <= 2**63 - 1
                or not isinstance(client_token, str) or not 1 <= len(client_token.encode()) <= 4096):
            raise ValueError('A validated local identity is required')
        if not isinstance(psk, str) or not 32 <= len(psk.encode()) <= 4096:
            raise ValueError('The local backend key must contain at least 32 bytes')
        if not 0 < timeout <= 30:
            raise ValueError('Invalid local backend timeout')
        self.session_id = _session_id()
        self._framer = RecordFramer()
        self._write_lock = threading.Lock()
        self._closed = False
        self._opened = False
        self.socket = socket.create_connection((host, port), timeout=timeout)
        try:
            key = psk.encode()
            self.socket.sendall(MAGIC + hmac.digest(key, MAGIC + b'\0gateway', 'sha256'))
            response = self._read_exact(len(MAGIC) + 32)
            expected = MAGIC + hmac.digest(key, MAGIC + b'\0gameserver', 'sha256')
            if not hmac.compare_digest(response, expected):
                raise BackendProtocolError('The local game logic service could not be verified')
            identity = struct.pack('<Q', user_id) + client_token.encode()
            digest = hmac.new(key, b'RF4-local-session\0' + identity, hashlib.sha256).hexdigest()
            token = f'local|{digest[:24]}|{user_id}|{digest[24:]}'
            payload = json.dumps({'token': token, 'reconnect': False, 'client_ip': host},
                                 separators=(',', ':')).encode()
            self._send(OPEN, self.session_id, payload)
            self._opened = True
        except BaseException:
            self.socket.close()
            self._closed = True
            raise

    def _read_exact(self, count):
        data = bytearray()
        while len(data) < count:
            chunk = self.socket.recv(count - len(data))
            if not chunk:
                raise EOFError('The local game logic service closed the tunnel')
            data.extend(chunk)
        return bytes(data)

    def _send(self, kind, session_id, payload):
        packet = _record(kind, session_id, payload)
        with self._write_lock:
            if self._closed:
                raise ConnectionError('The local game logic tunnel is closed')
            self.socket.sendall(packet)

    def send_data(self, payload):
        self._send(DATA, self.session_id, payload)

    def receive(self):
        """Read available bytes, return DATA/CLOSE records, and answer tunnel pings."""
        chunk = self.socket.recv(65536)
        if not chunk:
            if self._framer.buffer:
                raise BackendProtocolError('Truncated local backend record')
            raise EOFError('The local game logic service closed the tunnel')
        result = []
        for kind, session_id, payload in self._framer.feed(chunk):
            if kind in (PING, PONG):
                if session_id != 0 or payload:
                    raise BackendProtocolError('Invalid local backend keepalive')
                if kind == PING:
                    self._send(PONG, 0, b'')
            elif kind in (DATA, CLOSE) and session_id == self.session_id:
                result.append((kind, session_id, payload))
            else:
                raise BackendProtocolError('Unexpected local backend session record')
        return result

    def close(self):
        with self._write_lock:
            if self._closed:
                return
            try:
                if self._opened:
                    self.socket.sendall(_record(CLOSE, self.session_id, b'client_closed'))
            except OSError:
                pass
            finally:
                self._closed = True
                try:
                    self.socket.shutdown(socket.SHUT_RDWR)
                except OSError:
                    pass
                self.socket.close()

    def __enter__(self):
        return self

    def __exit__(self, *_):
        self.close()
