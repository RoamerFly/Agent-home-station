import json,urllib.request,urllib.parse,datetime
from pathlib import Path
root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
config=json.loads((root/'admin/config.json').read_text())
op=urllib.request.build_opener(urllib.request.ProxyHandler({}))
base='http://127.0.0.1:6174/v1/accounts/background-test-20261024'
def get(path):
 req=urllib.request.Request(base+path,headers={'Authorization':'Bearer '+(root/'api-token').read_text().strip()})
 with op.open(req,timeout=20)as r:return json.load(r)
result={'bot_enabled':config['enabled'],'character_enabled':config['companion']['enabled'],'max_age_seconds':config['max_age_seconds'],'cooldown_seconds':config['cooldown_seconds'],'groups':[]}
for g in get('/groups?limit=50')['items']:
 name=g.get('displayName')or g.get('name')or ''
 if not ('亲子' in name or '机器人测试' in name):continue
 gid=g.get('roomId')or g['id'];rule=next((r for r in config['group_rules'] if r['id']==gid),None)
 messages=get('/threads/'+urllib.parse.quote(gid,safe='')+'/messages?limit=50')['items']
 records=[]
 for m in sorted(messages,key=lambda m:m.get('createdAt',''),reverse=True)[:8]:
  detail=get('/messages/'+urllib.parse.quote(m['id'],safe=''))
  records.append({'created_at':m.get('createdAt'),'direction':m.get('direction'),'type':m.get('type'),'body_excerpt':str(m.get('body',''))[:160],'mentions':detail.get('mentions')})
 result['groups'].append({'name':name,'allowed':rule is not None,'rule':{'mode':rule.get('mode'),'prefix':rule.get('prefix')}if rule else None,'recent':records})
audit=json.loads((root/'admin/audit.json').read_text())
result['recent_events']=audit[-24:]
print(json.dumps(result,ensure_ascii=False,indent=2))
