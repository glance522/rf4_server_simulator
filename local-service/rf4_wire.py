"""Hermes transport framing; game RPC payloads remain opaque."""
from dataclasses import dataclass
import struct

MAX_FRAME = 16 * 1024 * 1024


@dataclass(frozen=True)
class Frame:
    kind: int
    sequence: int | None
    payload: bytes


def decode_body(body):
    if not body:
        raise ValueError('Empty Hermes frame')
    kind = body[0]
    if kind == 2 and len(body) == 1:
        return Frame(kind, None, b'')
    if kind not in (0, 1) or len(body) < 9 or (kind == 1 and len(body) != 9):
        raise ValueError('Invalid Hermes frame header')
    return Frame(kind, struct.unpack_from('<Q', body, 1)[0], body[9:])


class Framer:
    def __init__(self):
        self.buffer = bytearray()

    def feed(self, chunk):
        self.buffer.extend(chunk)
        frames = []
        while len(self.buffer) >= 4:
            length = struct.unpack_from('<I', self.buffer)[0]
            if length == 0:
                del self.buffer[:4]
                continue
            if not 1 <= length <= MAX_FRAME:
                raise ValueError('Hermes frame length is outside the limit')
            if len(self.buffer) < 4 + length:
                break
            body = bytes(self.buffer[4:4+length])
            del self.buffer[:4+length]
            frames.append(decode_body(body))
        return frames


def acknowledgement(sequence):
    return struct.pack('<IBQ', 9, 1, sequence)


def data_frame(sequence, payload):
    body = struct.pack('<BQ', 0, sequence) + payload
    if len(body) > MAX_FRAME:
        raise ValueError('Hermes frame is too large')
    return struct.pack('<I', len(body)) + body


class SessionCipher:
    """Client-compatible RC4 stream, with independent instances per direction."""
    def __init__(self, key):
        if not key:
            raise ValueError('Session key is empty')
        self.state = list(range(256))
        other = 0
        for index in range(256):
            other = (other + self.state[index] + key[index % len(key)]) & 255
            self.state[index], self.state[other] = self.state[other], self.state[index]
        self.index = self.other = 0

    def apply(self, data):
        result = bytearray()
        state = self.state
        for value in data:
            self.index = (self.index + 1) & 255
            self.other = (self.other + state[self.index]) & 255
            state[self.index], state[self.other] = state[self.other], state[self.index]
            result.append(value ^ state[(state[self.index] + state[self.other]) & 255])
        return bytes(result)
