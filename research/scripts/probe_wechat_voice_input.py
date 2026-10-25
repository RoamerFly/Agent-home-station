"""Verify the native voice download/decoding format, without sending messages."""
import json,time,urllib.request,urllib.parse
from pathlib import Path
root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024');workspace=Path(__file__).resolve().parents[2]
base='http://127.0.0.1:6174/v1/accounts/background-test-20261024'
token=(root/'api-token').read_text().strip();client=urllib.request.build_opener(urllib.request.ProxyHandler({}))
def call(path,body=None):
    req=urllib.request.Request(base+path,data=None if body is None else json.dumps(body).encode(),headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
    with client.open(req,timeout=15) as r:return json.load(r)
messages=call('/threads/filehelper/messages?limit=100')['items']
voices=[m for m in messages if m.get('type')==34]
if not voices:
    print('No voice in selected helper page; native format not live-proven.');raise SystemExit(0)
m=voices[-1];mid=m['id'];job=call('/messages/'+urllib.parse.quote(mid,safe='')+'/media-transfers',{'messageId':mid,'quality':'best'})
for _ in range(40):
    if job['state'] in ['completed','succeeded','available','failed','cancelled','needs_review']:break
    time.sleep(.3);job=call('/jobs/'+urllib.parse.quote(job['id'],safe=''))
report={'message_type':34,'direction':m.get('direction'),'transfer_state':job['state'],'wechat_sends':0}
if job.get('assetId'):
    req=urllib.request.Request(base+'/media/'+urllib.parse.quote(job['assetId'],safe='')+'/content',headers={'Authorization':'Bearer '+token})
    with client.open(req,timeout=15) as r:data=r.read(8*1024*1024);report.update(content_type=r.headers.get('Content-Type'),bytes=len(data),is_decoded_wav=data[:4]==b'RIFF' and data[8:12]==b'WAVE')
(workspace/'research/evidence/wechat-voice-input-format.json').write_text(json.dumps(report,indent=2),encoding='utf8');print(json.dumps(report))
