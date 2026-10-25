"""Real short generation diagnostic. Does not access or send WeChat messages."""
import json, time, urllib.request
from pathlib import Path
workspace=Path(__file__).resolve().parents[2]
token=(workspace/'secrets/yue-bridge-token.txt').read_text().strip()
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
base='http://172.29.160.1:6188'
def call(path,body=None):
    req=urllib.request.Request(base+path,data=json.dumps(body).encode() if body else None,
        headers={'Authorization':'Bearer '+token,'Content-Type':'application/json','Idempotency-Key':'service-recovery-20261025-short'})
    with opener.open(req,timeout=20) as r:return json.load(r)
print(json.dumps({'health':call('/health')}),flush=True)
job=call('/jobs',{'title':'服务恢复自检','style':'brief gentle female Chinese pop, complete short phrase','lyrics':'[verse]\n晚风吹过窗台\n轻轻唱出好心情\n[chorus]\n愿你一路平安','duration_seconds':10})
print(json.dumps({'diagnostic_job':job['id'],'state':job['state'],'wechat_sends':0}),flush=True)
deadline=time.monotonic()+600
while time.monotonic()<deadline:
    job=call('/jobs/'+job['id'])
    if job['state'] not in ('queued','running'):break
    time.sleep(2)
report={k:v for k,v in job.items() if k not in ('key','file')};report['wechat_sends']=0
(workspace/'research/evidence/yue-service-recovery.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(report,ensure_ascii=False),flush=True)
if job['state']!='completed':raise SystemExit(1)
