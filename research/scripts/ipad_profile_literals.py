"""Read fixed iPad profile globals and auth literal ranges from ELF; no execution."""
import hashlib
import json
from pathlib import Path
import struct

ROOT = Path(__file__).resolve().parents[2]
blob = (ROOT / 'research/samples/main.elf.bin').read_bytes()
phoff = struct.unpack_from('<Q', blob, 32)[0]
phsize, phcount = struct.unpack_from('<HH', blob, 54)
segments = []
for index in range(phcount):
    kind, _, offset, address, _, size, _, _ = struct.unpack_from('<II6Q', blob, phoff+index*phsize)
    if kind == 1:
        segments.append((address,address+size,offset))

def read(address,size):
    for lo,hi,offset in segments:
        if lo<=address and address+size<=hi:
            return blob[offset+address-lo:offset+address-lo+size]
    raise ValueError(hex(address))

def string_global(address):
    ptr,length=struct.unpack('<QQ',read(address,16))
    assert length<4096
    return dict(address=hex(address),pointer=hex(ptr),length=length,value=read(ptr,length).decode())

record=dict(authority='Static fixed ELF data and exact pointer/length strings; no target execution',
            sha256=hashlib.sha256(blob).hexdigest(),
            globals={name:string_global(address) for name,address in {
                'short_host':0x1a860d0,'long_host':0x1a860e0,'device_type':0x1a86130,
                'device_name':0x1a86140,'rom_model':0x1a86150,'os_version':0x1a86160}.items()},
            client_version=struct.unpack('<I',read(0x1aa9c58,4))[0],
            literals=[dict(address=hex(address),length=length,value=read(address,length).decode())
                      for address,length in [(0xf61522,4),(0xf62229,5),(0xf60713,3),(0xf62238,5),
                                             (0xf6040c,2),(0xf6ddcd,15),(0xf6cc85,14),(0xf85b8b,35),
                                             (0xf69a41,11),(0xf9fd0b,95),(0xf7e8b9,29),
                                             (0xf60309,1),(0xf6040e,2),(0xf6cc93,14),
                                             (0xf6cca1,14),(0xf6cc77,14)]])
(ROOT/'research/evidence/ipad-profile-literals.json').write_text(json.dumps(record,indent=2,ensure_ascii=False)+'\n',encoding='utf-8')
print(json.dumps(record,indent=2,ensure_ascii=False))
