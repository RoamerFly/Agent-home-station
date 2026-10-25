"""Send one idempotent playback-link card to the authorized file helper."""
import json,pathlib,time,urllib.error,urllib.parse,urllib.request
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024');workspace=pathlib.Path(__file__).resolve().parents[2]
config=json.loads((root/'admin/config.json').read_text());assert config['filehelper_enabled']and config['companion']['permissions']['send_video']
client=urllib.request.build_opener(urllib.request.ProxyHandler({}));token=(root/'api-token').read_text().strip();base='http://127.0.0.1:6174/v1/accounts/background-test-20261024'
def call(path,body=None,key=''):
    headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'}
    if key:headers['Idempotency-Key']=key
    with client.open(urllib.request.Request(base+path,data=None if body is None else json.dumps(body).encode(),headers=headers),timeout=15)as r:return json.load(r)
source='msg_eyJkYXRhYmFzZSI6Im1lc3NhZ2UvbWVzc2FnZV8wLmRiIiwidGFibGUiOiJNc2dfMjhlOGJlMWY0NjlhODEzODM5MzUzMjcwMGYxNWM3YWEiLCJsb2NhbElkIjoiNTIifQ'
message=call('/messages/'+urllib.parse.quote(source,safe=''));assert message['threadId']=='50583054216@chatroom'and message['link']
card=message['link'];u=urllib.parse.urlparse(card['url']);assert u.scheme in ['https','http']and u.hostname in ['www.bilibili.com','bilibili.com','b23.tv']
q=urllib.parse.parse_qs(u.query);url=urllib.parse.urlunparse(u._replace(query=urllib.parse.urlencode({'p':q['p'][0]})if q.get('p')else'',fragment=''))
key='video-playback-card-check-20261025'
try:state=call('/sends/by-idempotency-key?key='+key)
except urllib.error.HTTPError as e:
    if e.code!=404:raise
    state=call('/sends',{'kind':'share','threadId':'filehelper','title':'[测试] '+card['title'][:200],'url':url,'description':'播放页分享卡片测试；点击后由原网站播放。'},key)
for i in range(60):
    if state['state']in ['accepted','failed','needs_review','cancelled']:break
    time.sleep(1);state=call('/sends/'+urllib.parse.quote(state['id'],safe=''))
receipt=state.get('serverMessageId')or state.get('serverId')
report={'destination':'filehelper','kind':'share','state':state['state'],'id':state['id'],'has_wechat_receipt':bool(receipt),'source_is_user_shared_bili_page':True,'one_stable_idempotency_key':True,'video_file_uploaded':False,'phone_playback_verified':False}
(workspace/'research/evidence/video-share-card-live.json').write_text(json.dumps(report,indent=2),encoding='utf8');print(json.dumps(report))
assert state['state']=='accepted'and receipt
