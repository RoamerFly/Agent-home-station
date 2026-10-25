"""Release this local instance's stale update drain without replaying sends."""
import http.cookiejar,json,urllib.request
from pathlib import Path
root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024/admin')
base='http://127.0.0.1:8081'
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
csrf=''
def request(path,value=None):
    h={'Origin':base,'Content-Type':'application/json'}
    if csrf:h['X-CSRF-Token']=csrf
    req=urllib.request.Request(base+path,data=None if value is None else json.dumps(value).encode(),headers=h)
    with client.open(req,timeout=15) as r:return json.load(r)
csrf=request('/api/login',{'password':(root/'admin-token').read_text().strip()})['csrf']
before=request('/api/status')
if before['bot']['state']=='drained':
    ticket=request('/api/runtime/drain',{})['ticket']
    request('/api/runtime/undrain',{'ticket':ticket})
after=request('/api/status')
print(json.dumps({'before':before['bot'],'after':after['bot'],'version':after.get('version'),'wechat_logged_in':after['native']['authenticated']},ensure_ascii=False))
