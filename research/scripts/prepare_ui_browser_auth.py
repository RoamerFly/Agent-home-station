"""Create temporary localhost browser state without printing credentials. Delete after state-load."""
import http.cookiejar,json,urllib.request
from pathlib import Path
workspace=Path(__file__).resolve().parents[2]
base='http://127.0.0.1:8081'
jar=http.cookiejar.CookieJar();client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(jar))
req=urllib.request.Request(base+'/api/login',data=json.dumps({'password':(workspace/'secrets/web-local-admin-token.txt').read_text().strip()}).encode(),headers={'Origin':base,'Content-Type':'application/json'})
with client.open(req,timeout=10) as response:assert response.status==200
cookies=[{'name':c.name,'value':c.value,'domain':c.domain,'path':c.path,'expires':c.expires or -1,'httpOnly':True,'secure':c.secure,'sameSite':'Strict'}for c in jar]
p=workspace/'secrets/ui-qa-auth-state.json';assert not p.exists()
p.write_text(json.dumps({'cookies':cookies,'origins':[]}),encoding='utf8')
print('Private localhost browser state prepared; no credentials printed.')
