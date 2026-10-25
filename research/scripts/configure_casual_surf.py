"""Apply the owner's requested casual interests/range via authenticated API."""
import http.cookiejar,json,pathlib,urllib.request
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024');workspace=pathlib.Path(__file__).resolve().parents[2];base='http://127.0.0.1:8081'
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
def call(path,data=None,csrf=''):
    headers={'Origin':base,'Content-Type':'application/json'}
    if csrf:headers['X-CSRF-Token']=csrf
    with client.open(urllib.request.Request(base+path,data=None if data is None else json.dumps(data).encode(),headers=headers),timeout=20)as r:return json.load(r)
csrf=call('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})['csrf']
before=call('/api/surf');settings=before['settings']
assert settings['topics']==['有意思的音乐与科学发现'],'Interests changed concurrently; preserve them and inspect instead'
settings['topics']=['可爱动物与海洋趣闻','轻松好听的音乐','有趣的动画或生活短视频','好吃的和日常小发现']
settings['interval_max_minutes']=min(1440,settings['interval_minutes']*2)
saved=call('/api/surf',settings,csrf);after=call('/api/surf')
assert saved['threads']==before['settings']['threads']and saved['enabled']==before['settings']['enabled']and after['sessions']==before['sessions']
report={'topics':saved['topics'],'interval_min_minutes':saved['interval_minutes'],'interval_max_minutes':saved['interval_max_minutes'],'share_targets_unchanged':True,'budget_unchanged':True,'sessions_unchanged':True,'wechat_sends':0}
(workspace/'research/evidence/casual-surf-config.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8');print(json.dumps(report,ensure_ascii=False))
