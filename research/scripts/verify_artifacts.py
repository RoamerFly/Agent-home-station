"""Verify retained OCI bytes, layer digests and selected last-write files offline."""
import pathlib,json,base64,hashlib,tarfile
root=pathlib.Path('/mnt/e/A-Importance/wechat-bot/research')
cache=pathlib.Path('/mnt/c/Temp/wechat-bot-research-20261023/wechat-ipad-static')
sha=lambda b:'sha256:'+hashlib.sha256(b).hexdigest()
wrapper=json.loads((root/'evidence/manifest.json').read_text(encoding='utf-8-sig'))
raw=base64.b64decode(wrapper['Raw']); manifest=json.loads(raw)
out={'manifest':{'expected':wrapper['Descriptor']['digest'],'actual':sha(raw)},'layers':[],'artifacts':{}}
out['manifest']['match']=out['manifest']['expected']==out['manifest']['actual']
c=(root/'evidence/image-config.json').read_bytes()
out['config']={'expected':manifest['config']['digest'],'actual':sha(c),'match':manifest['config']['digest']==sha(c)}
targets={'usr/wic-go/main','usr/wic-go/lib/libv08.so','usr/wic-go/entrypoint.sh','usr/wic-go/swagger/swagger.json'}
for i,layer in enumerate(manifest['layers']):
    p=cache/f'layer-{i}.tar.gz'; actual=sha(p.read_bytes())
    out['layers'].append({'layer':i,'expected':layer['digest'],'actual':actual,'match':actual==layer['digest'],'bytes':p.stat().st_size})
    with tarfile.open(p,'r:gz') as tar:
        for member in tar:
            name=member.name.removeprefix('./').lstrip('/')
            if '/.wh.' in name:
                raise RuntimeError('Review OCI whiteout manually before claiming last-write provenance: '+name)
            if name in targets and member.isfile():
                out['artifacts'][name]={'last_layer':i,'sha256':sha(tar.extractfile(member).read()),'bytes':member.size}
for name,item in out['artifacts'].items():
    p=cache/'selected-files'/name
    item['selected_file_match']=p.exists() and sha(p.read_bytes())==item['sha256']
assert out['manifest']['match'] and out['config']['match']
assert len(out['layers'])==14 and all(x['match'] for x in out['layers'])
assert len(out['artifacts'])==4 and all(x['selected_file_match'] for x in out['artifacts'].values())
(root/'evidence/layer-verification.json').write_text(json.dumps(out,indent=2),encoding='utf8')
print('Verified manifest, config, all 14 layers and 4 selected files against final layer writes.')
