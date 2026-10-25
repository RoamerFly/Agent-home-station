"""Find candidate RIP-relative displacement references, then disassemble only hits."""
import contextlib,io,json,pathlib,runpy,struct,subprocess
ROOT=pathlib.Path(__file__).resolve().parents[2]
with contextlib.redirect_stdout(io.StringIO()):d=runpy.run_path(str(ROOT/'research/scripts/recover_newspam_v8_structs.py'))
b=d['blob'];hits=[];target=0x1b3b298
funcs=json.loads((ROOT/'research/evidence/main-functions.json').read_text())
for lo,hi,off in d['segments']:
    if lo>0xc00000:continue
    for at in range(off,off+hi-lo-4):
        disp=struct.unpack_from('<i',b,at)[0]
        if lo+at-off+4+disp==target:
            va=lo+at-off
            f=next((x for x in funcs if int(x['Entry'],16)<=va<int(x['End'],16)),None)
            hits.append({'displacement_va':hex(va),'function':f})
(ROOT/'research/evidence/spam-crc-table-xrefs.json').write_text(json.dumps(hits,indent=2)+'\n',encoding='utf8')
print(json.dumps(hits,indent=2))
