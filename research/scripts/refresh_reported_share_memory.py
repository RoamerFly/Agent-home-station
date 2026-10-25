"""Refresh derived card observations using new native metadata, with no sends."""
import http.cookiejar,json,pathlib,urllib.request
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024');workspace=pathlib.Path(__file__).resolve().parents[2];base='http://127.0.0.1:8081'
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
def call(path,data=None,csrf=''):
    headers={'Origin':base,'Content-Type':'application/json'}
    if csrf:headers['X-CSRF-Token']=csrf
    with client.open(urllib.request.Request(base+path,data=None if data is None else json.dumps(data).encode(),headers=headers),timeout=30)as r:return json.load(r)
csrf=call('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})['csrf'];scopes=call('/api/companion/state')['scopes'];report=[]
for scope in scopes:
    if scope['thread']not in ['43929704480@chatroom','50583054216@chatroom']:continue
    before=call('/api/companion/memory/'+scope['id']);identities=[(x.get('message_id'),x.get('sender'),x.get('time'))for x in before['messages']]
    result=call('/api/companion/memory/'+scope['id']+'/refresh-shares',{},csrf)
    after=call('/api/companion/memory/'+scope['id']);assert identities==[(x.get('message_id'),x.get('sender'),x.get('time'))for x in after['messages']]
    cards=[x for x in after['messages']if x['role']=='user'and '收到了一条分享卡片'in x['text']]
    assert all('没有可读取的标题或简介'not in x['text']for x in cards)
    report.append({'thread':scope['thread'],'refreshed':result['refreshed'],'card_observations':len(cards),'metadata_present':all('标题：'in x['text']for x in cards),'message_identity_and_time_unchanged':True,'wechat_sends':0})
(workspace/'research/evidence/share-memory-refreshed.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8');print(json.dumps(report,ensure_ascii=False))
