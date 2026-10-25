"""Read selected Go abi.StructType records from the pinned ELF, without execution.

The Go 1.24 amd64 ABI uses a 48-byte Type and 24-byte StructField. This
script validates sizes, field counts and address mappings, and includes the
raw addresses so inferred layouts can be checked against disassembly.
"""
import hashlib,json,pathlib,re,struct

ROOT=pathlib.Path(__file__).resolve().parents[2]
blob=(ROOT/'research/samples/main.elf.bin').read_bytes()
assert blob[:6]==b'\x7fELF\x02\x01'
phoff=struct.unpack_from('<Q',blob,32)[0]
phsize,phcount=struct.unpack_from('<HH',blob,54)
segments=[]
for i in range(phcount):
    kind,flags,pos,va,pa,fs,ms,align=struct.unpack_from('<II6Q',blob,phoff+i*phsize)
    if kind==1: segments.append((va,va+fs,pos))
def read(a,n):
    if n<0 or n>1<<20: raise ValueError('read bound')
    for lo,hi,pos in segments:
        if lo<=a and a+n<=hi:return blob[pos+a-lo:pos+a-lo+n]
    raise ValueError('address not file backed: '+hex(a))
def u64(a):return struct.unpack('<Q',read(a,8))[0]
shoff=struct.unpack_from('<Q',blob,40)[0]
shsize,shcount,namesindex=struct.unpack_from('<HHH',blob,58)
sections=[struct.unpack_from('<IIQQQQIIQQ',blob,shoff+i*shsize) for i in range(shcount)]
s=sections[namesindex]; names=blob[s[4]:s[4]+s[5]]
ro=next(s for s in sections if names[s[0]:].split(b'\0',1)[0]==b'.rodata')
# In this pinned Go ELF runtime.types starts 0x20 bytes into .rodata.
# Assert against multiple complete protobuf layouts below; do not assume the
# section address itself is the runtime name-offset base in other artifacts.
typesbase=ro[3]+0x20
def varint(a):
    n=0
    for i in range(5):
        v=read(a+i,1)[0];n|=(v&127)<<(7*i)
        if v<128:return n,a+i+1
    raise ValueError('name length varint')
def name(a):
    flags=read(a,1)[0];n,p=varint(a+1)
    if n>4096:raise ValueError('name length')
    text=read(p,n).decode('utf-8');p+=n
    tag=None
    if flags&2:
        n,p=varint(p)
        if n>4096:raise ValueError('tag length')
        tag=read(p,n).decode('utf-8')
    return text,tag
def typename(a):
    off=struct.unpack('<i',read(a+40,4))[0]
    if off<=0 or off>=ro[5]:raise ValueError('name offset')
    text,_=name(typesbase+off)
    # TFlagExtraStar means the name table stores one extra leading '*'.
    if read(a+20,1)[0]&2:
        if not text.startswith('*'):raise ValueError('extra-star flag')
        text=text[1:]
    return text
def layout(a):
    if read(a+23,1)[0]&31!=25:return None
    size=u64(a);fields=u64(a+56);n=u64(a+64);cap=u64(a+72)
    if size>1<<20 or n>256 or n!=cap:raise ValueError('struct bounds')
    result=[]
    for i in range(n):
        f=fields+i*24;fieldname,tag=name(u64(f));typ=u64(f+8);offset=u64(f+16)
        if offset>=size:raise ValueError('field offset')
        result.append({'name':fieldname,'offset':offset,'offset_hex':hex(offset),'type':typename(typ),'type_address':hex(typ),'tag':tag})
    return {'type_address':hex(a),'name':typename(a),'size':size,'fields':result}

source=(ROOT/'research/evidence/login-ghidra.txt').read_text(encoding='utf-8')
part=source.split('=== Login.SecManualAuth @',1)[1].split('\n=== ',1)[0]
addresses=sorted({int(x,16) for x in re.findall(r'&UNK_([0-9a-fA-F]+)',part)})
out=[];skipped=[]
for a in addresses:
    try:
        item=layout(a)
        if item:out.append(item)
    except (ValueError,UnicodeDecodeError) as e:skipped.append({'address':hex(a),'reason':str(e)})
if not out: raise ValueError(json.dumps({'unresolved':skipped}))
assert any(x['name'].endswith('.CryptoData') for x in out)
assert any(x['name'].endswith('.ManualAuthAesReqData') for x in out)
descriptor=json.loads((ROOT/'research/evidence/send-mm-descriptor.json').read_text(encoding='utf-8'))
messages={m['name']:m for m in descriptor['messageType']}
checked=0
aliases={'SKBuiltinStringT':'SKBuiltinString_t','SKBuiltinBufferT':'SKBuiltinBuffer_t'}
for item in out:
    localname=item['name'].split('.')[-1]
    msg=messages[aliases.get(localname,localname)]
    expected={int(f['number']):f['name'] for f in msg['field']}
    observed={}
    for f in item['fields']:
        if f['tag'] is None:continue
        match=re.search(r'protobuf:"[^,]+,(\d+),[^\"]*name=([^,\"]+)',f['tag'])
        if match:observed[int(match[1])]=match[2]
    assert observed==expected,(item['name'],observed,expected)
    checked+=len(observed)
payload={'method':'static Go1.24 amd64 abi.Type/StructType records; no target execution','sample_sha256':hashlib.sha256(blob).hexdigest(),'types_base':hex(typesbase),'selection':'type operands in Login.SecManualAuth decompilation','descriptor_cross_check':{'messages':len(out),'fields':checked,'all_tag_names_and_numbers_match':True},'structures':out,'unresolved_operands':skipped}
target=ROOT/'research/evidence/login-go-struct-layouts.json'
target.write_text(json.dumps(payload,ensure_ascii=False,indent=2),encoding='utf-8')
print(json.dumps({'structures':[{'name':x['name'],'address':x['type_address'],'fields':len(x['fields'])} for x in out],'unresolved_operands':skipped},indent=2))
