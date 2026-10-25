"""Extract just the regular 08sae.dat member from digest-verified OCI layers.

No tar paths are extracted to the filesystem and no target program is launched.
Later layers are checked for replacement/whiteout before selecting final bytes.
"""
import pathlib,json,base64,hashlib,tarfile
ROOT=pathlib.Path(__file__).resolve().parents[2]
CACHE=pathlib.Path('C:/Temp/wechat-bot-research-20261023/wechat-ipad-static')
wrapper=json.loads((ROOT/'research/evidence/manifest.json').read_text(encoding='utf-8-sig'))
raw=base64.b64decode(wrapper['Raw']);manifest=json.loads(raw)
sha=lambda b:'sha256:'+hashlib.sha256(b).hexdigest()
assert sha(raw)==wrapper['Descriptor']['digest']
target='usr/wic-go/08sae.dat';last=None;value=None
for index,layer in enumerate(manifest['layers']):
    path=CACHE/f'layer-{index}.tar.gz'
    assert sha(path.read_bytes())==layer['digest']
    with tarfile.open(path,'r:gz') as tar:
        for member in tar:
            name=member.name.removeprefix('./').lstrip('/')
            if '/.wh.' in name:raise RuntimeError('Manual whiteout review required: '+name)
            if name!=target:continue
            if not member.isfile() or member.size>8<<20:raise RuntimeError('Invalid SAE data member')
            value=tar.extractfile(member).read();last=index
assert value is not None
out=ROOT/'research/samples/08sae.dat.bin'
if out.exists():assert out.read_bytes()==value
else:out.write_bytes(value)
record={'authority':'selected last regular-file write in frozen digest-verified OCI layers; static data only','manifest_digest':sha(raw),'path':target,'last_layer':last,'bytes':len(value),'sha256':sha(value),'executed':False}
(ROOT/'research/evidence/sae08-extraction.json').write_text(json.dumps(record,indent=2),encoding='utf-8')
print(json.dumps(record,indent=2))
