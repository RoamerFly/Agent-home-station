"""Read recent unknown group identities from native messages, no chat sends."""
import json,urllib.request
from pathlib import Path
from urllib.parse import quote
root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
config=json.loads((root/'admin/config.json').read_text())
known={x['id'] for x in config['group_rules']}
rows=json.loads((root/'admin/activity.json').read_text())
unknown=[r for r in rows if r.get('thread_id','').endswith('@chatroom') and r['thread_id'] not in known][-4:]
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
token=(root/'api-token').read_text().strip();base='http://127.0.0.1:6174/v1/accounts/background-test-20261024'
def get(path):
    with opener.open(urllib.request.Request(base+path,headers={'Authorization':'Bearer '+token}),timeout=15) as r:return json.load(r)
results=[]
for r in unknown:
    item={k:r.get(k) for k in ('time','thread_id','event','message_id','reason','status')}
    try:
        m=get('/messages/'+quote(r['message_id'],safe=''))
        item['message']={k:m.get(k) for k in ('type','threadId','senderId','body')}
        if item['message'].get('body'):item['message']['body']=item['message']['body'][:180]
        item['group']=get('/groups/'+quote(r['thread_id'],safe=''))
    except Exception as e:item['read_error']=type(e).__name__
    results.append(item)
print(json.dumps(results,ensure_ascii=False,indent=2))
