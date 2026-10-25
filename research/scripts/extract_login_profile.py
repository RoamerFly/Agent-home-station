"""Recover static profile globals and Client.Init bootstrap key. Never executes ELF."""
import pathlib,struct,json,hashlib
ROOT=pathlib.Path(__file__).resolve().parents[2]
b=(ROOT/'research/samples/main.elf.bin').read_bytes()
segments=[]
off=struct.unpack_from('<Q',b,32)[0];size,count=struct.unpack_from('<HH',b,54)
for i in range(count):
    kind,flags,pos,va,pa,fs,ms,align=struct.unpack_from('<II6Q',b,off+i*size)
    if kind==1:segments.append((va,va+fs,pos))
def read(a,n):
    for s,e,p in segments:
        if s<=a and a+n<=e:return b[p+a-s:p+a-s+n]
    raise ValueError(hex(a))
def gostring(a):
    ptr,n=struct.unpack('<QQ',read(a,16))
    assert n<512
    return {'header_address':hex(a),'data_address':hex(ptr),'length':n,'text':read(ptr,n).decode()}
profile={
    'authority':'ELF file-backed globals, not runtime-mutated profile values',
    'artifact_sha256':hashlib.sha256(b).hexdigest(),
    'client_init':'0x7d1520..0x7d1f40',
    'ios_init_references':{'version_load':'0x7d16ca','device_type_load':'0x7d16d3..0x7d16da','bootstrap_key_load':'0x7d1702..0x7d1709'},
    'ipad_client_version':struct.unpack('<I',read(0x1aa9c58,4))[0],
    'ipad_device_type':gostring(0x1a86130),
    'bootstrap_public_key_hex':read(0xfa1542,130).decode(),
}
assert len(bytes.fromhex(profile['bootstrap_public_key_hex']))==65
(ROOT/'research/evidence/login-static-profile.json').write_text(json.dumps(profile,indent=2),encoding='utf8')
print(json.dumps(profile,indent=2))
