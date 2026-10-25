"""Independent literal x86 shift/OR model for static-reconstruction vectors.

Does not load or execute the target library. Values establish consistency of two
independent implementations of the recovered instructions, not native parity.
"""
import hashlib
import json
from pathlib import Path
import random

ROOT = Path(__file__).resolve().parents[2]
MASK = 0xffffffff


def shl(x, n):
    return (x << (n & 31)) & MASK


def shr(x, n):
    return (x & MASK) >> (n & 31)


def model(data, constant, timestamp):
    out = bytearray()
    for i in range(len(data) // 4):
        x = int.from_bytes(data[4*i:4*i+4], 'little')
        x = shr(x, 31-i) | shl(x, i+1)
        mask_t = shr(timestamp, i) | shl(timestamp, -i)
        mask_c = shl(constant, i) | shr(constant, -i)
        x ^= mask_t ^ mask_c
        x = shr(x, 31-i) | shl(x, i+1)
        out.extend(x.to_bytes(4, 'little'))
    for i, x in enumerate(data[len(data)//4*4:]):
        x = (shl(x, i+1) | shr(x, 8-(i+1))) & 255
        if i == 0:
            x ^= (timestamp ^ constant) & 255
        else:
            t = shr(timestamp & 255, i) | shl(timestamp, (-i) & 7)
            c = shl(constant & 255, i) | shr(constant & 255, (-i) & 7)
            x ^= (t ^ c) & 255
        out.append((shr(x, i ^ 7) | shl(x, i+1)) & 255)
    return out.hex()


def main():
    rand = random.Random(0x763038)
    strings = []
    params = [(0, 0), (0xffffffff, 0xffffffff), (0x12345678, 0x89abcdef), (1, 0x80000000)]
    for length in [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 124, 127, 128, 129, 130, 131, 132, 133, 255, 256, 257]:
        data = bytes(rand.randrange(256) for _ in range(length))
        for constant, timestamp in params:
            strings.append(dict(input=data.hex(), constant=constant, timestamp=timestamp,
                                output=model(data, constant, timestamp)))
    for data in [b'\x00a\x00b\x00', '微信'.encode(), b'\xff\x80\x7f\x00']:
        constant, timestamp = params[2]
        strings.append(dict(input=data.hex(), constant=constant, timestamp=timestamp,
                            output=model(data, constant, timestamp)))
    integers = []
    for value in [0, 1, 0xffffffff, 0x100000000, 0xffffffffffffffff, 0x0123456789abcdef, 1 << 63]:
        for constant, timestamp in params:
            output = bytes.fromhex(model(value.to_bytes(8, 'little'), constant, timestamp))
            integers.append(dict(input=str(value), constant=constant, timestamp=timestamp,
                                 output=str(int.from_bytes(output, 'little'))))
    target = ROOT / 'research/samples/libv08.so.bin'
    result = dict(provenance='Static literal-instruction model; not original-oracle output',
                  target_sha256=hashlib.sha256(target.read_bytes()).hexdigest(),
                  strings=strings, integers=integers)
    destination = ROOT / 'internal/v08/testdata/static-vectors.json'
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text(json.dumps(result, indent=2) + '\n', encoding='utf-8')
    print(f'{len(strings)} string vectors; {len(integers)} integer vectors')


if __name__ == '__main__':
    main()
