"""Read-only member directory diagnosis; never sends a message or prints keys."""
import json,urllib.request,urllib.error
from pathlib import Path
root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
token=(root/'api-token').read_text().strip()
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
out=[]
config=json.loads((root/'admin/config.json').read_text())
for rule in config['group_rules']:
    group=rule['id']
    url='http://127.0.0.1:6174/v1/accounts/background-test-20261024/groups/'+group+'/members?limit=50'
    try:
        with opener.open(urllib.request.Request(url,headers={'Authorization':'Bearer '+token}),timeout=15) as r:
            data=json.load(r)
            out.append({'group':group,'http':r.status,'shape':{k:type(v).__name__ for k,v in data.items()},'count':len(data.get('items',[])),'partial':data.get('coverage',{}).get('partial'),'first_item_fields':list(data.get('items',[{}])[0]) if data.get('items') else []})
    except urllib.error.HTTPError as e:
        data=json.load(e);out.append({'group':group,'http':e.code,'error':data})
print(json.dumps(out,ensure_ascii=False,indent=2))
