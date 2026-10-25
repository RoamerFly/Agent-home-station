"""Offline ELF reads for Ghidra-selected Go string pointer/length pairs."""
import json, struct, pathlib, bisect, subprocess, re
root = pathlib.Path('/mnt/e/A-Importance/wechat-bot/research')
sample = pathlib.Path('/mnt/c/Temp/wechat-bot-research-20261023/wechat-ipad-static/selected-files/usr/wic-go/main')
b = sample.read_bytes()
phoff = struct.unpack_from('<Q', b, 32)[0]
entsz, count = struct.unpack_from('<HH', b, 54)
segments = []
for i in range(count):
    kind, flags, off, va, pa, fs, ms, al = struct.unpack_from('<II6Q', b, phoff+i*entsz)
    if kind == 1:
        segments.append((va, va+fs, off))
def read(va, length):
    for start, end, off in segments:
        if start <= va and va+length <= end:
            return b[off+va-start:off+va-start+length]
    raise ValueError(hex(va))
pairs = [(0xf68368,10),(0xf6fda4,17),(0xf710ff,18),(0xf7a846,26),(0xf7a860,26),
         (0xf64b38,7),(0xf6abad,12),(0xf60d4e,4),(0xf69b28,11),(0xf6cd2d,14),
         (0xf6071c,3),(0xf6c049,13),(0xf61b3e,4),(0xf67dd0,9),(0xf7c18e,26)]
out = {'method':'ELF PT_LOAD mapping; exact byte lengths from selected pseudocode. No execution.',
       'strings':[{'address':hex(a),'length':n,'text':read(a,n).decode('utf8')} for a,n in pairs]}
out['pointers'] = [{'address':hex(a),'u64':hex(struct.unpack('<Q',read(a,8))[0])}
                   for a in (0x1a844a8,0x12b1a98,0x12b1060,0x12b2a30)]
(root/'evidence/main-exact-literals.json').write_text(json.dumps(out,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(out,ensure_ascii=False,indent=2))
# Locate direct x86 rel32 call candidates and verify them against objdump's decoded
# instruction stream from the enclosing Go function entry, avoiding data matches.
funcs=json.loads((root/'evidence/main-functions.json').read_text())
entries=[int(f['Entry'],16) for f in funcs]
hits=[]
for start,end,off in segments:
    pos=off
    while True:
        pos=b.find(b'\xe8',pos,off+end-start)
        if pos<0 or pos+5>len(b):break
        va=start+pos-off
        target=va+5+struct.unpack_from('<i',b,pos+1)[0]
        pos+=1
        if target!=0xba6780:continue
        idx=bisect.bisect_right(entries,va)-1
        if idx<0 or va>=int(funcs[idx]['End'],16):continue
        f=funcs[idx]
        asm=subprocess.check_output(['objdump','-d','--start-address='+f['Entry'],'--stop-address='+f['End'],str(sample)],text=True)
        line=next((s for s in asm.splitlines() if re.match(r'\s*'+format(va,'x')+r':',s)), '')
        if re.search(r'\bcall\s+ba6780\b',line):hits.append({'from':hex(va),'function':f['Name'],'instruction':line})
(root/'evidence/uuid-direct-callers.json').write_text(json.dumps({'target':'0xba6780','verified_direct_calls':hits,'scope':'No indirect-call or runtime reachability claim.'},indent=2),encoding='utf8')
print('UUID direct call sites:',hits)
