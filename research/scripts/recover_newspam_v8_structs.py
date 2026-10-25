"""List Go StructType operands referenced by GetNewSpamDataV8, statically."""
import pathlib, re, struct

root = pathlib.Path(__file__).resolve().parents[2]
blob = (root / 'research/samples/main.elf.bin').read_bytes()
phoff = struct.unpack_from('<Q', blob, 32)[0]
phsize, phcount = struct.unpack_from('<HH', blob, 54)
segments = []
for i in range(phcount):
    kind, _, pos, va, _, size, _, _ = struct.unpack_from('<II6Q', blob, phoff + i * phsize)
    if kind == 1:
        segments.append((va, va + size, pos))
def read(a, n):
    for lo, hi, pos in segments:
        if lo <= a and a + n <= hi:
            return blob[pos + a - lo:pos + a - lo + n]
    raise ValueError(hex(a))
def u64(a): return struct.unpack('<Q', read(a, 8))[0]
shoff = struct.unpack_from('<Q', blob, 40)[0]
shsize, shcount, ni = struct.unpack_from('<HHH', blob, 58)
sections = [struct.unpack_from('<IIQQQQIIQQ', blob, shoff + i * shsize) for i in range(shcount)]
names = blob[sections[ni][4]:sections[ni][4] + sections[ni][5]]
ro = next(x for x in sections if names[x[0]:].split(b'\0', 1)[0] == b'.rodata')
base = ro[3] + 0x20
def varint(a):
    value = 0
    for i in range(5):
        b = read(a + i, 1)[0]; value |= (b & 127) << (7 * i)
        if b < 128: return value, a + i + 1
    raise ValueError('varint')
def name(a):
    flags = read(a, 1)[0]; n, a = varint(a + 1); s = read(a, n).decode(); a += n
    if flags & 2:
        n, a = varint(a); tag = read(a, n).decode()
    else: tag = None
    return s, tag
def typename(a):
    off = struct.unpack('<i', read(a + 40, 4))[0]; s, _ = name(base + off)
    return s[1:] if read(a + 20, 1)[0] & 2 else s
def dump(a):
    if read(a + 23, 1)[0] & 31 != 25: return
    size, fields, count = u64(a), u64(a + 56), u64(a + 64)
    if not (0 < size < 1 << 20 and count <= 256): return
    print(hex(a), typename(a), 'size', size)
    for i in range(count):
        f = fields + i * 24; n, tag = name(u64(f))
        print(' ', hex(u64(f + 16)), n, typename(u64(f + 8)), tag)

src = (root / 'research/evidence/device-v8-ghidra.txt').read_text(encoding='utf-8')
part = src.split('=== wechatdll/clientsdk/extinfo.GetNewSpamDataV8 @', 1)[1].split('\n=== ', 1)[0]
for address in sorted({int(x, 16) for x in re.findall(r'&UNK_([0-9a-fA-F]+)', part)}):
    try: dump(address)
    except (ValueError, UnicodeDecodeError): pass
