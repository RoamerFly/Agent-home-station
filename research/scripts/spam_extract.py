"""Recover selected literal operands and FileInfo construction from frozen ELF.
No executable code is run. The generated JSON records exact operand addresses.
"""
import contextlib,io,json,pathlib,re,runpy
ROOT=pathlib.Path(__file__).resolve().parents[2]
with contextlib.redirect_stdout(io.StringIO()):d=runpy.run_path(str(ROOT/'research/scripts/recover_newspam_v8_structs.py'))
read=d['read']; text=(ROOT/'research/evidence/device-v8-dependencies-ghidra.txt').read_text(encoding='utf8')
part=text.split('=== wechatdll/clientsdk/extinfo.GetFileInfo @')[1]
chunks=re.split(r'\n  (puStack_[a-zA-Z0-9]+) = \(undefined8 \*\)func_0x0041c140\(&UNK_00d86b60\);',part)
items=[]
for i in range(1,len(chunks),2):
    name,body=chunks[i:i+2]
    if name=='puStack_10':pass
    if 'func_0x00461a00' in body:
        m=re.search(r'func_0x00461a00\(0,&UNK_([0-9a-f]+),(0x[0-9a-f]+|\d+),uStack0000000000000008,uStack0000000000000010,\s*&UNK_([0-9a-f]+),(0x[0-9a-f]+|\d+)\)',body)
        assert m, name
        a,n,b,k=(int(x,16) if j in (0,2) else int(x,0) for j,x in enumerate(m.groups()))
        items.append({'stack':name,'prefix':read(a,n).decode(),'suffix':read(b,k).decode(),'operands':[hex(a),n,hex(b),k]})
    else:
        m=re.search(r'\(&UNK_([0-9a-f]+)\)\[iVar2\]',body)
        if not m:break
        a=int(m[1],16);m=re.search(r'if \((0x[0-9a-f]+|\d+) < iVar2\)',body);assert m
        n=int(m[1],0)+1
        items.append({'stack':name,'literal':read(a,n).decode(),'operands':[hex(a),n]})
assert len(items)==30,len(items)
out={'authority':'literal operands and construction order from GetFileInfo@b45500; static ELF bytes; string XOR emits Go rune UTF8','items':items}
(ROOT/'research/evidence/spam-fileinfo-literals.json').write_text(json.dumps(out,indent=2)+'\n',encoding='utf8')
spam=(ROOT/'research/evidence/device-v8-ghidra.txt').read_text(encoding='utf8').split('=== wechatdll/clientsdk/extinfo.GetNewSpamDataV8 @')[1].split('\n=== ')[0]
literals=[]
for m in re.finditer(r'func_0x007bd760\(&UNK_([0-9a-f]+),(0x[0-9a-f]+|\d+),(0x[0-9a-f]+|\d+),',spam):
    a,n,key=int(m[1],16),int(m[2],0),int(m[3],0)
    literals.append({'offset':hex(a),'bytes':n,'mask':hex(key),'value':read(a,n).decode()})
(ROOT/'research/evidence/spam-later-literals.json').write_text(json.dumps(literals,indent=2)+'\n',encoding='utf8')
print(json.dumps(out,indent=2))
