"""Offline Go ABI recovery for Newinit's request/response structs."""
import hashlib, json, pathlib, struct

ROOT = pathlib.Path(__file__).resolve().parents[2]
blob = (ROOT / "research/samples/main.elf.bin").read_bytes()
assert blob[:6] == b"\x7fELF\x02\x01"
phoff = struct.unpack_from("<Q", blob, 32)[0]
phsize, phcount = struct.unpack_from("<HH", blob, 54)
segments = []
for i in range(phcount):
    kind, flags, pos, va, pa, fs, ms, align = struct.unpack_from("<II6Q", blob, phoff+i*phsize)
    if kind == 1: segments.append((va, va+fs, pos))
def read(a, n):
    if n < 0 or n > 1 << 20: raise ValueError("bounded read")
    for lo, hi, pos in segments:
        if lo <= a and a+n <= hi: return blob[pos+a-lo:pos+a-lo+n]
    raise ValueError("not file backed: " + hex(a))
def u64(a): return struct.unpack("<Q", read(a, 8))[0]
shoff = struct.unpack_from("<Q", blob, 40)[0]
shsize, shcount, namesindex = struct.unpack_from("<HHH", blob, 58)
sections = [struct.unpack_from("<IIQQQQIIQQ", blob, shoff+i*shsize) for i in range(shcount)]
names = blob[sections[namesindex][4]:sections[namesindex][4]+sections[namesindex][5]]
ro = next(s for s in sections if names[s[0]:].split(b"\0", 1)[0] == b".rodata")
typesbase = ro[3] + 0x20
def varint(a):
    result = 0
    for i in range(5):
        v = read(a+i, 1)[0]; result |= (v & 127) << (7*i)
        if v < 128: return result, a+i+1
    raise ValueError("overlong name varint")
def name(a):
    flags = read(a, 1)[0]; n, p = varint(a+1)
    if n > 4096: raise ValueError("name too long")
    txt = read(p, n).decode(); p += n; tag = None
    if flags & 2:
        n, p = varint(p)
        if n > 4096: raise ValueError("tag too long")
        tag = read(p, n).decode()
    return txt, tag
def typename(a):
    off = struct.unpack("<i", read(a+40, 4))[0]
    txt, _ = name(typesbase+off)
    return txt[1:] if read(a+20, 1)[0] & 2 else txt
def layout(a):
    if read(a+23, 1)[0] & 31 != 25: raise ValueError("not struct")
    size, fields, n, cap = u64(a), u64(a+56), u64(a+64), u64(a+72)
    if size > 1<<20 or n > 256 or n != cap: raise ValueError("struct bounds")
    out=[]
    for i in range(n):
        p=fields+i*24; fn, tag=name(u64(p)); typ=u64(p+8); off=u64(p+16)
        out.append({"name":fn,"offset":off,"offset_hex":hex(off),"type":typename(typ),"type_address":hex(typ),"tag":tag})
    return {"type_address":hex(a),"name":typename(a),"size":size,"fields":out}
addresses=[0xee9c20,0xef1e60,0xeaa660,0xf06c00,0xf47440,0xf55600,0xeaf8a0,0xf22420,0xeaf7c0,0xef4a20,0xf42740]
out=[layout(a) for a in addresses]
target=ROOT/'research/evidence/newinit-go-layouts.json'
target.write_text(json.dumps({"method":"offline Go1.24 ABI StructType parse", "sample_sha256":hashlib.sha256(blob).hexdigest(),"types_base":hex(typesbase),"structures":out},ensure_ascii=False,indent=2),encoding='utf-8')
for s in out: print(s['type_address'],s['name'],s['size'],len(s['fields']))
