"""Retrieve only changed, digest-verified OCI layers for static version analysis.
Never launches an image, extracts tar paths, or sends any account material.
"""
import urllib.request,urllib.error,urllib.parse,re,json,hashlib,pathlib,tarfile
ROOT=pathlib.Path(__file__).resolve().parents[2]
OUT=ROOT/'research/evidence/version-update';OUT.mkdir(exist_ok=True)
BASE='https://registry.cn-shenzhen.aliyuncs.com';REPO='houhou/wechat-ipad'
sha=lambda b:'sha256:'+hashlib.sha256(b).hexdigest()
url=BASE+'/v2/'+REPO+'/manifests/latest'
try:urllib.request.urlopen(url,timeout=20).close();raise RuntimeError('unexpected unchallenged registry')
except urllib.error.HTTPError as e:
    if e.code!=401:raise
    parts=dict(re.findall(r'(\w+)="([^"]*)"',e.headers.get('WWW-Authenticate','')))
realm=parts['realm'];assert realm.startswith('https://')
with urllib.request.urlopen(realm+'?'+urllib.parse.urlencode({'service':parts['service'],'scope':parts.get('scope','repository:'+REPO+':pull')}),timeout=20) as r:token=json.load(r)['token']
def fetch(path,limit=64<<20):
    req=urllib.request.Request(BASE+'/v2/'+REPO+'/'+path,headers={'Authorization':'Bearer '+token,'Accept':'application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json'})
    with urllib.request.urlopen(req,timeout=40) as r:
        b=r.read(limit+1);digest=r.headers.get('Docker-Content-Digest')
    if len(b)>limit:raise ValueError('bounded registry read exceeded')
    return b,digest
raw,digest=fetch('manifests/latest',1<<20);assert sha(raw)==digest
manifest=json.loads(raw);(OUT/'manifest.json').write_bytes(raw)
config,_=fetch('blobs/'+manifest['config']['digest'],2<<20);assert sha(config)==manifest['config']['digest']
(OUT/'config.json').write_bytes(config)
old=json.loads((ROOT/'research/evidence/manifest.json').read_text(encoding='utf-8-sig'))['OCIManifest']
oldDigests={x['digest'] for x in old['layers']}
targets={'usr/wic-go/main':'main.elf.bin','usr/wic-go/08sae.dat':'08sae.dat.bin','usr/wic-go/rqtx.dat':'rqtx.dat.bin','usr/wic-go/lib/libv08.so':'libv08.so.bin'}
record={'manifest_digest':digest,'created':json.loads(config).get('created'),'changed_layers':[],'selected':{},'executed':False}
for i,layer in enumerate(manifest['layers']):
    if layer['digest'] in oldDigests:continue
    print('Fetching changed layer',i,layer['size'],flush=True)
    path=OUT/f'layer-{i}.tar.gz'
    if path.exists():data=path.read_bytes()
    else:data,_=fetch('blobs/'+layer['digest']);path.write_bytes(data)
    assert sha(data)==layer['digest']
    info={'index':i,'digest':layer['digest'],'bytes':len(data)};record['changed_layers'].append(info)
    with tarfile.open(path,'r:gz') as tar:
        for member in tar:
            name=member.name.removeprefix('./').lstrip('/')
            if '/.wh.' in name:raise RuntimeError('manual whiteout review required')
            if name not in targets:continue
            if not member.isfile() or member.size>64<<20:raise RuntimeError('unsupported target member')
            b=tar.extractfile(member).read();(OUT/targets[name]).write_bytes(b)
            record['selected'][name]={'layer':i,'sha256':sha(b),'bytes':len(b),'local':targets[name]}
(OUT/'extraction.json').write_text(json.dumps(record,indent=2),encoding='utf-8')
print(json.dumps(record,indent=2))
