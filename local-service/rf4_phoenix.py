"""Verified Phoenix values, requests (-1), results (-2), and failures (-3)."""
from dataclasses import dataclass
import struct
from uuid import UUID


class Int64(int):
    """Keep the native Int64 tag for small identifiers."""


class EnumInt(int):
    """Keep the native boxed-enum tag."""


class Float32(float):
    """Keep the native Single tag."""


class UnsupportedValue(ValueError):
    def __init__(self, tag, offset, type_id=None):
        super().__init__('Phoenix value type is not implemented')
        self.tag, self.offset, self.type_id = tag, offset, type_id


@dataclass(frozen=True)
class TypedArray:
    alias: str
    values: list


@dataclass(frozen=True)
class Message:
    request_id: int
    code: int
    subcode: int
    arguments: TypedArray | None
    flag: bool


@dataclass(frozen=True)
class Result:
    request_id: int
    value: object


@dataclass(frozen=True)
class Failure:
    request_id: int
    error_type: str | None
    message: str | None


@dataclass(frozen=True)
class StringArray:
    values: list[str | None]


@dataclass(frozen=True)
class GuidArray:
    values: list[UUID]


@dataclass(frozen=True)
class NativeValue:
    """A checked native game snapshot with its original encoding."""
    data: bytes


@dataclass(frozen=True)
class IntTriple133:
    a: int
    b: int
    c: int


@dataclass(frozen=True)
class Record309:
    """Fields follow the verified wire order."""
    f1: float
    f2: float
    f3: float
    f4: float
    f5: float
    f6: float
    f7: float
    i1: int
    i2: int


class Reader:
    def __init__(self, data):
        self.data = memoryview(data)
        self.offset = 0

    def take(self, count):
        if count < 0 or count > len(self.data) - self.offset:
            raise ValueError('Truncated Phoenix value')
        result = self.data[self.offset:self.offset+count]
        self.offset += count
        return result

    def number(self, form):
        return struct.unpack(form, self.take(struct.calcsize(form)))[0]

    def short_string(self):
        length = self.number('<B')
        return None if length == 255 else bytes(self.take(length)).decode('utf-8')

    def long_string(self):
        length = self.number('<i')
        return None if length == 0x7fffffff else bytes(self.take(length)).decode('utf-8')

    def value(self, depth=0):
        if depth > 32:
            raise ValueError('Phoenix nesting is too deep')
        offset = self.offset
        tag = self.number('<B')
        if tag == 0:
            return None
        if tag in (14, 15):
            return tag == 14
        if tag == 12:
            return UUID(bytes_le=bytes(self.take(16)))
        if tag == 23:
            return UUID(int=0)
        if tag == 8:
            length = self.number('<i')
            return bytes(self.take(length))
        if tag in (13, 16):
            return self.number('<i')
        if tag == 17:
            return Int64(self.number('<q'))
        if tag == 18:
            return self.number('<d')
        if tag == 19:
            return Float32(self.number('<f'))
        if tag == 20:
            return self.short_string()
        if tag == 21:
            length = self.number('<H')
            return None if length == 65535 else bytes(self.take(length)).decode('utf-8')
        if tag == 22:
            return self.long_string()
        if tag == 3:
            alias = self.short_string()
            if alias is None:
                raise ValueError('Phoenix array type is missing')
            count = self.number('<H')
            if count > len(self.data) - self.offset:
                raise ValueError('Truncated Phoenix array')
            return TypedArray(alias, [self.value(depth+1) for _ in range(count)])
        if tag == 4:
            count = self.number('<H')
            if count > (len(self.data)-self.offset)//4:
                raise ValueError('Truncated Phoenix string array')
            return StringArray([self.long_string() for _ in range(count)])
        if tag == 6:
            count = self.number('<H')
            if count > (len(self.data)-self.offset)//16:
                raise ValueError('Truncated Phoenix GUID array')
            return GuidArray([UUID(bytes_le=bytes(self.take(16))) for _ in range(count)])
        if tag == 1:
            type_id = self.number('<i')
            if type_id == -2:
                return Result(self.number('<i'), self.value(depth+1))
            if type_id == -3:
                return Failure(self.number('<i'), self.long_string(), self.long_string())
            if type_id == 133:
                c, a, b = self.number('<i'), self.number('<i'), self.number('<i')
                return IntTriple133(a, b, c)
            if type_id == 309:
                return Record309(*struct.unpack('<7f2i', self.take(36)))
            if type_id == -1:
                request_id = self.number('<i')
                code, subcode = self.number('<B'), self.number('<B')
                arguments = self.value(depth+1)
                if arguments is not None and not isinstance(arguments, TypedArray):
                    raise ValueError('Phoenix message arguments are not an array')
                flag = self.number('<B')
                if flag not in (0, 1):
                    raise ValueError('Phoenix message flag is invalid')
                return Message(request_id, code, subcode, arguments, bool(flag))
            raise UnsupportedValue(tag, offset, type_id)
        raise UnsupportedValue(tag, offset)


def decode(data):
    reader = Reader(data)
    value = reader.value()
    if reader.offset != len(reader.data):
        raise ValueError('Phoenix value has trailing data')
    return value


def short_string(value):
    if value is None:
        return b'\xff'
    raw = value.encode('utf-8')
    if len(raw) > 254:
        raise ValueError('Long Phoenix strings are not implemented')
    return bytes([len(raw)]) + raw


def long_string(value):
    if value is None:
        return struct.pack('<i', 0x7fffffff)
    raw = value.encode('utf-8')
    return struct.pack('<i', len(raw)) + raw


def encode(value):
    if value is None:
        return b'\x00'
    if isinstance(value, bool):
        return b'\x0e' if value else b'\x0f'
    if isinstance(value, NativeValue):
        return value.data
    if isinstance(value, bytes):
        return b'\x08' + struct.pack('<i', len(value)) + value
    if isinstance(value, Int64):
        return b'\x11' + struct.pack('<q', value)
    if isinstance(value, EnumInt):
        return b'\x0d' + struct.pack('<i', value)
    if isinstance(value, Float32):
        return b'\x13' + struct.pack('<f', value)
    if isinstance(value, float):
        return b'\x12' + struct.pack('<d', value)
    if isinstance(value, UUID):
        return b'\x0c' + value.bytes_le if value.int else b'\x17'
    if isinstance(value, int):
        return b'\x10' + struct.pack('<i', value) if -(2**31) <= value < 2**31 else b'\x11' + struct.pack('<q', value)
    if isinstance(value, str):
        raw = value.encode('utf-8')
        if len(raw) <= 254:
            return b'\x14' + short_string(value)
        if len(raw) <= 65534:
            return b'\x15' + struct.pack('<H', len(raw)) + raw
        return b'\x16' + long_string(value)
    if isinstance(value, Message):
        return (b'\x01' + struct.pack('<iiBB', -1, value.request_id, value.code, value.subcode)
                + encode(value.arguments) + bytes([value.flag]))
    if isinstance(value, Result):
        return b'\x01' + struct.pack('<ii', -2, value.request_id) + encode(value.value)
    if isinstance(value, Failure):
        return (b'\x01' + struct.pack('<ii', -3, value.request_id)
                + long_string(value.error_type) + long_string(value.message))
    if isinstance(value, StringArray):
        return b'\x04' + struct.pack('<H', len(value.values)) + b''.join(long_string(item) for item in value.values)
    if isinstance(value, GuidArray):
        return b'\x06' + struct.pack('<H', len(value.values)) + b''.join(item.bytes_le for item in value.values)
    if isinstance(value, IntTriple133):
        return b'\x01' + struct.pack('<iiii', 133, value.c, value.a, value.b)
    if isinstance(value, Record309):
        return (b'\x01' + struct.pack('<i7f2i', 309, value.f1, value.f2, value.f3,
                                     value.f4, value.f5, value.f6, value.f7, value.i1, value.i2))
    if isinstance(value, TypedArray):
        return (b'\x03' + short_string(value.alias) + struct.pack('<H', len(value.values))
                + b''.join(encode(item) for item in value.values))
    raise TypeError('Phoenix value type is not implemented')
