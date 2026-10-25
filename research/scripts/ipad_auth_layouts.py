"""Focused read-only ABI layouts for iPad login routing and auth fields."""
import json
from pathlib import Path
import struct

ROOT=Path(__file__).resolve().parents[2]
decoder=ROOT/'research/scripts/recover_go_structs.py'
definitions=decoder.read_text(encoding='utf-8').split('source=(ROOT/',1)[0]
ns={'__file__':str(decoder)}
exec(compile(definitions,str(decoder),'exec'),ns)
read=ns['read']
layouts=[ns['layout'](address) for address in [0xef1e60,0xefdf80,0xf38c00,0xf50740]]
links=next(s for s in ns['sections'] if ns['names'][s[0]:].split(b'\0',1)[0]==b'.typelink')
for offset in range(0,links[5],4):
    relative=struct.unpack('<i',read(links[3]+offset,4))[0]
    address=ns['typesbase']+relative
    try:
        name=ns['typename'](address)
        if 'Login.GetQRReq' not in name:continue
        if read(address+23,1)[0]&31==22:address=ns['u64'](address+48)
        item=ns['layout'](address)
        if item is not None and item['name'] not in [x['name'] for x in layouts]:layouts.append(item)
    except (ValueError,UnicodeDecodeError):continue
record={'authority':'Static fixed Go1.24 amd64 ABI; no target execution','structures':layouts}
(ROOT/'research/evidence/ipad-auth-layouts.json').write_text(json.dumps(record,indent=2,ensure_ascii=False)+'\n',encoding='utf-8')
for item in layouts:
 print(item['name'],item['type_address'])
 if 'GetQRReq' in item['name']:
  for field in item['fields']:print(field)
