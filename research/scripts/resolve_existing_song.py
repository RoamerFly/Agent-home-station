"""Reconcile the authorized recovery with a later, already delivered song."""
import json,http.cookiejar,urllib.request
from pathlib import Path
root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
workspace=Path(__file__).resolve().parents[2]
base='http://127.0.0.1:8081';csrf=''
c=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
def call(path,body=None):
    req=urllib.request.Request(base+path,data=None if body is None else json.dumps(body).encode(),headers={'Origin':base,'Content-Type':'application/json','X-CSRF-Token':csrf})
    with c.open(req,timeout=20) as r:return json.load(r)
csrf=call('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})['csrf']
initial=json.loads((workspace/'research/evidence/authorized-song-recovery.json').read_text())
task=call('/api/agent/tasks/'+initial['id'],{'action':'cancel'})
print(json.dumps({k:task.get(k) for k in ['id','state','music_job']},ensure_ascii=False))
data=call('/api/agent')
existing=next(t for t in data['tasks'] if t.get('music_job')=='9bb292dc40b4342a5e554c8a0539eabe')
state=json.loads((root/'bot-state/messages-v2.json').read_text())
media=[m for m in state.get('media',[]) if m.get('trace','').startswith('music-'+existing['id'])]
assert len(media)==1 and media[0]['state']=='accepted', 'Existing song does not have complete delivery proof'
private=json.loads((root/'admin/speech-config.json').read_text())
req=urllib.request.Request(private['yue_endpoint']+'/jobs/'+existing['music_job'],headers={'Authorization':'Bearer '+private['yue_token']})
with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req,timeout=15) as r:job=json.load(r)
report={'recovery_task_state':task['state'],'reason':'Later user followup already generated and delivered the requested song; duplicate recovery cancelled before execution','existing_task':{k:existing.get(k)for k in ['id','music_job','state','origin_trace']},'generation':{k:job.get(k)for k in ['id','state','duration_seconds','requested_seconds']},'media':[{'state':m['state'],'title':m['title'],'status':m.get('status')}for m in media]}
(workspace/'research/evidence/authorized-song-recovery-reconciled.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf-8')
print(json.dumps(report,ensure_ascii=False,indent=2))
