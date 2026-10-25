"""Recover only the user's explicitly authorized exact original song request."""
import json, http.cookiejar, urllib.request
from pathlib import Path
root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
base='http://127.0.0.1:8081'
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
csrf=''
def call(path,body=None):
    headers={'Origin':base,'Content-Type':'application/json','X-CSRF-Token':csrf}
    req=urllib.request.Request(base+path,data=None if body is None else json.dumps(body).encode(),headers=headers)
    with client.open(req,timeout=30) as response:return json.load(response)
csrf=call('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})['csrf']
rows=call('/api/logs')['items']
matches=[r for r in rows if r.get('category')=='received' and '给我唱一首两分钟左右的，适合下班听的中文歌曲' in r.get('content','')]
assert matches,'Exact authorized incoming message was not found; no action'
msg=matches[-1]
assert msg['thread_id']=='43929704480@chatroom','Original scope changed; no action'
task=call('/api/agent/recover-music-message',{'message_id':msg['message_id']})
evidence={k:task.get(k) for k in ['id','thread','state','recovery_message_id','origin_trace','music_job','music_kind','title']}
evidence['original_request']=msg['content']
target=Path(__file__).resolve().parents[1]/'evidence/authorized-song-recovery.json'
target.write_text(json.dumps(evidence,ensure_ascii=False,indent=2),encoding='utf-8')
print(json.dumps(evidence,ensure_ascii=False))
