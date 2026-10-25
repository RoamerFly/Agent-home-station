import json,http.cookiejar,urllib.request
from pathlib import Path
root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
base='http://127.0.0.1:8081';client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
def get(path,value=None):
 headers={'Origin':base};body=None
 if value is not None:body=json.dumps(value).encode();headers['Content-Type']='application/json'
 with client.open(urllib.request.Request(base+path,data=body,headers=headers),timeout=20)as r:return json.load(r)
get('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})
status=get('/api/status');rows=get('/api/logs')['items']
print(json.dumps({'bot':status['bot'],'recent_activity':[{k:r.get(k) for k in ['time','category','source','content','reason','status']}for r in rows if r.get('category')not in ['system','tool']][-16:]},ensure_ascii=False,indent=2))
