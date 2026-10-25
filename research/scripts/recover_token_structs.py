"""Selected typelink/ABI layouts for token requests. Does not execute ELF.

Reuse only the read/ABI definitions above the entry point of the pinned-layout
decoder. Its top-level extraction/report-writing entry point is not executed.
"""
import pathlib,json,struct,hashlib
ROOT=pathlib.Path(__file__).resolve().parents[2]
decoder=ROOT/'research/scripts/recover_go_structs.py'
definitions=decoder.read_text(encoding='utf-8').split('source=(ROOT/',1)[0]
ns={'__file__':str(decoder)};exec(compile(definitions,str(decoder),'exec'),ns)
read,layout=ns['read'],ns['layout']
sections,names=ns['sections'],ns['names']
links=next(s for s in sections if names[s[0]:].split(b'\0',1)[0]==b'.typelink')
selected=('comm.LoginData','baseinfo.DeviceInfo','TrustDeviceInfo','TrustResponseData','TrustSoftData','TrustReq','TrustData','FPFresh','ZTData','SaeInfo','baseinfo.Stat')
seen={}
for offset in range(0,links[5],4):
    relative=struct.unpack('<i',read(links[3]+offset,4))[0]
    address=ns['typesbase']+relative
    try:
        name=ns['typename'](address)
        if not any(q in name for q in selected):continue
        if read(address+23,1)[0]&31==22:address=ns['u64'](address+48)
        item=layout(address)
        if item is not None:seen[item['name']]=item
    except (ValueError,UnicodeDecodeError):continue
out={'method':'static typelink enumeration using pinned Go1.24 amd64 ABI and ELF; no target execution','artifact_sha256':hashlib.sha256(ns['blob']).hexdigest(),'structures':list(seen.values())}
(ROOT/'research/evidence/device-token-go-layouts.json').write_text(json.dumps(out,indent=2),encoding='utf-8')
for item in seen.values():print(item['type_address'],item['name'],len(item['fields']))
