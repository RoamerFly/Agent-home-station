"""Copy an explicitly requested group's settings through authenticated APIs."""
import argparse,copy,http.cookiejar,json,urllib.request
from pathlib import Path
parser=argparse.ArgumentParser();parser.add_argument('--source',required=True);parser.add_argument('--target',required=True);args=parser.parse_args()
root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024/admin')
base='http://127.0.0.1:8081';client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
csrf=''
def request(path,body=None):
    headers={'Origin':base,'Content-Type':'application/json'}
    if body is not None and csrf:headers['X-CSRF-Token']=csrf
    req=urllib.request.Request(base+path,data=None if body is None else json.dumps(body).encode(),headers=headers)
    with client.open(req,timeout=30) as r:return json.load(r)
csrf=request('/api/login',{'password':(root/'admin-token').read_text().strip()})['csrf']
cfg=request('/api/config');original=copy.deepcopy(cfg)
source=next(x for x in cfg['group_rules'] if x['name']==args.source)
from urllib.parse import quote
groups=request('/api/groups?search='+quote(args.target))['items']
target=next(x for x in groups if x['name']==args.target)
sid,tid=source['id'],target['id']
rule=copy.deepcopy(source);rule.update(id=tid,name=args.target)
cfg['group_rules']=[x for x in cfg['group_rules'] if x['id']!=tid]+[rule]
companion=cfg['companion'];selected=companion.get('proactive_threads',[])
companion['proactive_threads']=[x for x in selected if x!=tid]+([tid] if sid in selected else [])
rules=companion.get('proactive_rules',[])
companion['proactive_rules']=[x for x in rules if x['thread']!=tid]+[dict(x,thread=tid) for x in rules if x['thread']==sid]
surf=request('/api/surf')['settings'];agent=request('/api/agent')['settings']
backup={'config':original,'surf':copy.deepcopy(surf),'agent':copy.deepcopy(agent)}
# These API views contain no credentials; still keep the target mapping private.
backup_path=root/'group-copy-before.json';backup_path.write_text(json.dumps(backup,ensure_ascii=False),encoding='utf8');backup_path.chmod(0o600)
if request('/api/config')!=original:raise RuntimeError('Configuration changed concurrently; no write performed')
request('/api/config',cfg)
threads=surf['threads'];surf['threads']=[x for x in threads if x!=tid]+([tid] if sid in threads else [])
request('/api/surf',surf)
threads=agent['privileged_threads'];agent['privileged_threads']=[x for x in threads if x!=tid]+([tid] if sid in threads else [])
if agent!=backup['agent']:request('/api/agent/config',agent)
saved=request('/api/config');saved_rule=next(x for x in saved['group_rules'] if x['id']==tid)
assert {k:v for k,v in saved_rule.items() if k not in ('id','name')}=={k:v for k,v in source.items() if k not in ('id','name')}
assert request('/api/config')['key_configured']==original['key_configured']
print(json.dumps({'target':args.target,'rule':saved_rule,'proactive_selected':tid in companion['proactive_threads'],'surf_selected':tid in surf['threads'],'privileged_thread':tid in agent['privileged_threads'],'wechat_sends':0},ensure_ascii=False,indent=2))
