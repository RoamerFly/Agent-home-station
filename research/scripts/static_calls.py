"""Read ELF bytes and invoke objdump only; never execute the inspected image."""
import json,struct,subprocess,re,bisect,pathlib
root=pathlib.Path('/mnt/e/A-Importance/wechat-bot/research')
p=pathlib.Path('/mnt/c/Temp/wechat-bot-research-20261023/wechat-ipad-static/selected-files/usr/wic-go/main')
b=p.read_bytes();phoff=struct.unpack_from('<Q',b,32)[0];entsz,num=struct.unpack_from('<HH',b,54)
segments=[]
for i in range(num):
 t,flags,off,va,pa,fs,ms,al=struct.unpack_from('<II6Q',b,phoff+i*entsz)
 if t==1:segments.append((va,va+fs,off))
def peek(va):
 for s,e,o in segments:
  if s<=va<e:return b[o+va-s:o+va-s+180]
 return b''
funcs=json.loads((root/'evidence/main-functions.json').read_text(encoding='utf-8-sig'));entries=[int(f['Entry'],16) for f in funcs]
def name(va):
 i=bisect.bisect_right(entries,va)-1
 return funcs[i]['Name'] if i>=0 and va<int(funcs[i]['End'],16) else '?'
out=[]
for row in (root/'evidence/main-selected.tsv').read_text().splitlines():
 a,n=row.split('\t');f=next(f for f in funcs if f['Name']==n)
 text=subprocess.check_output(['objdump','-d','--start-address='+a,'--stop-address='+f['End'],str(p)],text=True)
 out.append('\n=== '+n+' '+a+' ===\n'+text)
 out.append('ANNOTATED CALLS AND DATA CANDIDATES (data read is not execution):')
 for line in text.splitlines():
  m=re.search(r'\bcall\s+([0-9a-f]+)\s',line)
  if m:out.append(line+' => '+name(int(m[1],16)))
  m=re.search(r'\blea\s+.*#\s+([0-9a-f]+)',line)
  if m:
   v=peek(int(m[1],16));v=v.split(b'\0')[0]
   if len(v)>2 and all(x in (9,10,13) or x>=32 for x in v):out.append(line+' => BYTES '+repr(v.decode('utf8','replace')))
(root/'evidence/main-selected-disassembly.txt').write_text('\n'.join(out),encoding='utf8')
print('Inspected',len(out),'blocks; result saved')
