import json,http.cookiejar,urllib.request
from pathlib import Path
root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
workspace=Path(__file__).resolve().parents[2]
base='http://127.0.0.1:8081'
c=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
def call(path,body=None):
    req=urllib.request.Request(base+path,data=None if body is None else json.dumps(body).encode(),headers={'Origin':base,'Content-Type':'application/json'})
    with c.open(req,timeout=20) as r:return json.load(r)
call('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})
initial=json.loads((workspace/'research/evidence/authorized-song-recovery.json').read_text())
parent=initial['id'];thread=initial['thread']
data=call('/api/agent')
tasks=[t for t in data['tasks'] if t['id']==parent or t.get('origin_trace')=='recover-'+parent]
jobs=[]
private=json.loads((root/'admin/speech-config.json').read_text())
for t in tasks:
    if t.get('music_job'):
        req=urllib.request.Request(private['yue_endpoint']+'/jobs/'+t['music_job'],headers={'Authorization':'Bearer '+private['yue_token']})
        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req,timeout=15) as r:
            job=json.load(r)
        jobs.append({k:job.get(k) for k in ['id','state','duration_seconds','requested_seconds','reason']})
rows=call('/api/logs')['items']
relevant=[{k:r.get(k) for k in ['time','event','category','content','reason','status','tool','message_id','send_id']} for r in rows if r.get('thread_id')==thread and (r.get('message_id')=='recover-'+parent or any(r.get('message_id','').startswith('music-'+t['id']) for t in tasks if t.get('music_job')) or r.get('event')=='music_generation_finished')]
report={'tasks':[{k:t.get(k) for k in ['id','title','state','music_job','music_kind','music_title','music_prepared','last_result']}for t in tasks],'jobs':jobs,'activity':relevant[-18:]}
(workspace/'research/evidence/authorized-song-recovery-progress.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf-8')
print(json.dumps(report,ensure_ascii=False,indent=2))
