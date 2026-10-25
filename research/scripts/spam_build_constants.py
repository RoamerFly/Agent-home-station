"""Record file-backed build globals referenced by GetNewSpamDataV8."""
import contextlib,io,json,pathlib,runpy,struct
ROOT=pathlib.Path(__file__).resolve().parents[2]
with contextlib.redirect_stdout(io.StringIO()):d=runpy.run_path(str(ROOT/'research/scripts/recover_newspam_v8_structs.py'))
out={'authority':'frozen ELF file-backed string headers and bytes, not runtime-mutated memory','artifact_sha256':'9b0f99ad74f11426aeb50b1c543cfadf3562338336d15dd4bc20e306089b0b2f','values':[]}
for name,addr in [('HeadMD5',0x1a86360),('IOSBuildVersion',0x1a860f0),('KernelType',0x1a86100),('KernelVersion',0x1a86110),('KernelRelease',0x1a86120)]:
    p,n=struct.unpack('<QQ',d['read'](addr,16));out['values'].append({'name':name,'header':hex(addr),'data':hex(p),'length':n,'value':d['read'](p,n).decode()})
(ROOT/'research/evidence/spam-build-constants.json').write_text(json.dumps(out,indent=2)+'\n',encoding='utf8')
print(json.dumps(out,indent=2))
