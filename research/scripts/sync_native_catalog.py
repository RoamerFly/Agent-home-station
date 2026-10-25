"""Explicit metadata refresh; no chat sends or reply-permission changes."""
import argparse,json,time,urllib.request
from pathlib import Path
from urllib.parse import quote
parser=argparse.ArgumentParser();parser.add_argument('--kind',choices=['groups','contacts'],default='groups');parser.add_argument('--search',default='');parser.add_argument('--identity',default='');args=parser.parse_args()
root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
base='http://127.0.0.1:6174/v1/accounts/background-test-20261024'
token=(root/'api-token').read_text().strip()
client=urllib.request.build_opener(urllib.request.ProxyHandler({}))
def request(path,create=False):
    req=urllib.request.Request(base+path,data=b'{}' if create else None,headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
    with client.open(req,timeout=15) as response:return json.load(response)
before=request('/'+args.kind+'?limit=100&search='+quote(args.search))
print(json.dumps({'before_matches':len(before.get('items',[]))}),flush=True)
route='/'+args.kind+'/'+quote(args.identity,safe='')+'/refresh' if args.identity else '/'+('group-sync' if args.kind=='groups' else 'contact-sync')
op=request(route,True)
print(json.dumps({'sync_operation':op.get('id'),'state':op.get('state')}),flush=True)
deadline=time.monotonic()+150
while op.get('state') in ('queued','running','starting') and time.monotonic()<deadline:
    time.sleep(1);op=request('/operations/'+quote(op['id']))
after=request('/'+args.kind+'?limit=100&search='+quote(args.search))
report={'kind':args.kind,'id':op.get('id'),'state':op.get('state'),'result':op.get('result'),'matched_items':[{k:v for k,v in x.items() if k in ('id','roomId','displayName','name','isInGroup')} for x in after.get('items',[])],'wechat_sends':0}
Path(__file__).resolve().parents[2].joinpath('research/evidence/catalog-sync-latest.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(report,ensure_ascii=False,indent=2),flush=True)
