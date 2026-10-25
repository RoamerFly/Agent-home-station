"""Inspect immutable SendPostData template copied by SendNewMsg; no execution."""
import pathlib,struct,json
root=pathlib.Path(__file__).resolve().parents[2]
b=(root/'research/samples/main.elf.bin').read_bytes()
phoff=struct.unpack_from('<Q',b,32)[0];entsz,count=struct.unpack_from('<HH',b,54)
segments=[]
for i in range(count):
    kind,flags,off,va,pa,fs,ms,al=struct.unpack_from('<II6Q',b,phoff+i*entsz)
    if kind==1:segments.append((va,va+fs,off))
def read(va,n):
    for s,e,o in segments:
        if s<=va and va+n<=e:return b[o+va-s:o+va-s+n]
    raise ValueError(hex(va))
base=0x13d8328
words=[]
for off in range(0,0x100,8):
    value=struct.unpack('<Q',read(base+off,8))[0]
    row={'offset':hex(off),'address':hex(base+off),'u64':value,'hex':hex(value)}
    try:
        length=struct.unpack('<Q',read(base+off+8,8))[0]
        if 0<length<512:
            data=read(value,length)
            if all(32<=x<127 for x in data):row['possible_go_string']=data.decode('ascii')
    except ValueError:pass
    words.append(row)
out={'template_address':hex(base),'copy_site':'0xb61a92..0xb61ad3','words':words,
     'limit':'template bytes require structure-offset mapping and overwrite checks before naming all fields'}
(root/'research/evidence/send-template.json').write_text(json.dumps(out,indent=2),encoding='utf8')
print(json.dumps(out,indent=2))
