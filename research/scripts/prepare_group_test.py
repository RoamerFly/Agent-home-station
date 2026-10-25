"""Resolve the user-approved group by exact name; never send a message."""
import json
import os
from pathlib import Path
import urllib.parse
import urllib.request
root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
token=(root/'api-token').read_text().strip()
group_name='机器人测试群'
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
query=urllib.parse.urlencode({'search':group_name,'limit':100})
req=urllib.request.Request('http://127.0.0.1:6174/v1/accounts/background-test-20261024/groups?'+query,headers={'Authorization':'Bearer '+token})
with opener.open(req,timeout=20) as r: page=json.load(r)
matches=[g for g in page['items'] if g.get('displayName')==group_name]
if len(matches)!=1:
    print(json.dumps({'exact_group_matches':len(matches),'has_more':bool(page.get('nextCursor'))}));raise SystemExit(1)
group=matches[0]
group_id=group.get('roomId') or group.get('id')
assert isinstance(group_id,str) and group_id.endswith('@chatroom') and len(group_id)<128
path=root/'approved-group-test.json'
fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600)
with os.fdopen(fd,'w') as f:json.dump({'group_name':group_name,'thread_id':group_id},f)
print(json.dumps({'exact_group_matches':1,'group_allowlist_saved':True,'group_messages_sent':0},ensure_ascii=False))
