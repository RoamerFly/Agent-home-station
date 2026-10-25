"""Generate literal tables from the separately retained exact ELF operands."""
import json,pathlib
ROOT=pathlib.Path(__file__).resolve().parents[2]
items=json.loads((ROOT/'research/evidence/spam-fileinfo-literals.json').read_text())['items']
out=['// Generated literal values from spam-fileinfo-literals.json; no original code.','package deviceproof','var spamFiles = [...]struct{suffix,uuid string}{']
for i in range(0,len(items),2):
    assert items[i]['prefix']=='/var/containers/Bundle/Application/'
    out.append('{'+json.dumps(items[i]['suffix'])+','+json.dumps(items[i+1]['literal'])+'},')
out.append('}')
later=json.loads((ROOT/'research/evidence/spam-later-literals.json').read_text())
for name,mask in [('spamAppex', '0x540cf80d'),('spamFrameworks','0x8c36ee2a')]:
    out.append('var '+name+' = [...]string{')
    for v in later:
        if v['mask']==mask:out.append(json.dumps(v['value'])+',')
    out.append('}')
(ROOT/'internal/deviceproof/spam_file_literals.go').write_text('\n'.join(out)+'\n',encoding='utf8')
