"""Print selected Go struct layouts from the pinned ELF without executing it."""
import pathlib
import struct

root = pathlib.Path(__file__).resolve().parents[2]
blob = (root / "research/samples/main.elf.bin").read_bytes()
phoff = struct.unpack_from("<Q", blob, 32)[0]
phsize, phcount = struct.unpack_from("<HH", blob, 54)
segments = []
for i in range(phcount):
    kind, _, pos, va, _, size, _, _ = struct.unpack_from("<II6Q", blob, phoff + i * phsize)
    if kind == 1:
        segments.append((va, va + size, pos))

def read(address, size):
    for lo, hi, file_offset in segments:
        if lo <= address and address + size <= hi:
            return blob[file_offset + address - lo:file_offset + address - lo + size]
    raise ValueError(hex(address))

def u64(address): return struct.unpack("<Q", read(address, 8))[0]

shoff = struct.unpack_from("<Q", blob, 40)[0]
shsize, shcount, names_index = struct.unpack_from("<HHH", blob, 58)
sections = [struct.unpack_from("<IIQQQQIIQQ", blob, shoff + i * shsize) for i in range(shcount)]
names_section = sections[names_index]
names = blob[names_section[4]:names_section[4] + names_section[5]]
rodata = next(s for s in sections if names[s[0]:].split(b"\0", 1)[0] == b".rodata")
types_base = rodata[3] + 0x20

def varint(address):
    value = 0
    for i in range(5):
        byte = read(address + i, 1)[0]
        value |= (byte & 127) << (7 * i)
        if byte < 128: return value, address + i + 1
    raise ValueError("varint")

def name(address):
    flags = read(address, 1)[0]
    size, address = varint(address + 1)
    value = read(address, size).decode()
    address += size
    tag = None
    if flags & 2:
        size, address = varint(address)
        tag = read(address, size).decode()
    return value, tag

def typename(address):
    offset = struct.unpack("<i", read(address + 40, 4))[0]
    value, _ = name(types_base + offset)
    if read(address + 20, 1)[0] & 2:
        value = value[1:]
    return value

def dump(address):
    size = u64(address)
    fields = u64(address + 56)
    count = u64(address + 64)
    print(hex(address), typename(address), "size", size, "fields", count)
    for i in range(count):
        field = fields + i * 24
        field_name, tag = name(u64(field))
        print(" ", hex(u64(field + 16)), field_name, typename(u64(field + 8)), tag)

for address in (0xedf020, 0xef7360, 0xf1efa0):
    dump(address)
