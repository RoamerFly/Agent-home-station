"""Read scheduling metadata only; do not print messages, IDs or credentials."""
import collections,datetime,json,pathlib
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
p=root/'bot-state/messages-v2.json'
data=json.loads(p.read_text()) if p.exists() else {'inbox':[],'deliveries':[]}
now=datetime.datetime.now(datetime.timezone.utc)
active=[]
for item in data.get('inbox',[]):
    if item.get('phase') in ('processing','sending','tool_running','read_pending','received'):
        stamp=datetime.datetime.fromisoformat(item['updated'].replace('Z','+00:00'))
        active.append({'phase':item['phase'],'age_seconds':round((now-stamp).total_seconds())})
print(json.dumps({'phase_counts':dict(collections.Counter(x.get('phase') for x in data.get('inbox',[]))),'active_metadata':active,'pending_deliveries':sum(x.get('state') not in ('accepted','stopped') for x in data.get('deliveries',[]))}))
