"""Export an image-only portable release. Runtime volumes/secrets are excluded."""
from datetime import datetime, timezone
import gzip
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import zipfile

workspace=Path(__file__).resolve().parents[1]
release=workspace/'dist/releases/wechat-bot-web-0.3.0-linux-amd64'
release.mkdir(parents=True,exist_ok=True)
image='wechat-bot-web:0.3.0'
info=json.loads(subprocess.check_output(['docker','image','inspect',image],text=True))[0]
assert info['Os']=='linux' and info['Architecture']=='amd64'
archive=release/'wechat-bot-web_0.3.0_linux-amd64.tar.gz'
temporary=archive.with_suffix('.partial')
process=subprocess.Popen(['docker','image','save',image],stdout=subprocess.PIPE)
with temporary.open('wb') as raw:
    with gzip.GzipFile(filename='',mode='wb',fileobj=raw,compresslevel=6,mtime=0) as compressed:
        shutil.copyfileobj(process.stdout,compressed,1<<20)
process.stdout.close()
if process.wait()!=0:
    raise SystemExit('docker image export failed; partial archive retained')
temporary.replace(archive)
for source,target in [('compose.web.yaml','compose.web.yaml'),('DEPLOY-WEB.md','README.md')]:
    shutil.copyfile(workspace/source,release/target)
manifest={
    'created_at_utc':datetime.now(timezone.utc).isoformat(),
    'image':image,'image_id':info['Id'],'os':info['Os'],'architecture':info['Architecture'],
    'archive_bytes':archive.stat().st_size,'image_bytes':info['Size'],
    'native_source':'https://github.com/thisnick/agent-wechat',
    'native_commit':'7095ef01e0cd3d5b567bcb6970f379dcd88bbfa8',
    'runtime_volumes_exported':False,'account_or_api_credentials_exported':False,
}
(release/'manifest.json').write_text(json.dumps(manifest,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
checks=[]
for name in [archive.name,'compose.web.yaml','README.md','manifest.json']:
    digest=hashlib.file_digest((release/name).open('rb'),'sha256').hexdigest()
    checks.append(digest+'  '+name)
(release/'SHA256SUMS').write_text('\n'.join(checks)+'\n',encoding='utf-8')
bundle=release.parent/(release.name+'.zip')
with zipfile.ZipFile(bundle,'w',compression=zipfile.ZIP_STORED) as output:
    for name in [archive.name,'compose.web.yaml','README.md','manifest.json','SHA256SUMS']:
        output.write(release/name,arcname=name)
print(json.dumps({'image':image,'image_id':info['Id'],'image_archive':str(archive),'archive_bytes':archive.stat().st_size,'deployment_bundle':str(bundle),'bundle_bytes':bundle.stat().st_size}))
