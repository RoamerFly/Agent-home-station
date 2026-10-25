"""One new quote test to own filehelper; stable key avoids duplicate submits."""
import pathlib,urllib.request,urllib.error,urllib.parse,http.cookiejar,json,time
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024');workspace=pathlib.Path(__file__).resolve().parents[2]
base='http://127.0.0.1:8081';csrf=''
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
def call(path,value=None):
 headers={'Origin':base,'X-CSRF-Token':csrf};data=None
 if value is not None:data=json.dumps(value).encode();headers['Content-Type']='application/json'
 try:
  with opener.open(urllib.request.Request(base+path,data=data,headers=headers),timeout=70)as r:return r.status,json.load(r)
 except urllib.error.HTTPError as e:return e.code,json.load(e)
csrf=call('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})[1]['csrf']
key='quote-050-own-filehelper-20261024'
code,state=call('/api/tools/lookup?key='+key)
if code==404:
 code,page=call('/api/catalog?kind=messages&thread=filehelper')
 assert code==200
 candidates=[m for m in page['items']if m.get('type')==1 and m.get('serverId')and m.get('id')]
 assert candidates,'No self-filehelper text to quote'
 source=candidates[-1]
 code,state=call('/api/tools/send',{'thread_id':'filehelper','kind':'text','text':'[测试] 微信引用回复链路验证','quote':{'message_id':source['id'],'server_id':source['serverId']},'idempotency_key':key})
assert code in(200,202),(code,state.get('error'))
deadline=time.monotonic()+90
while state['state']in['queued','running','sending','submitting']and time.monotonic()<deadline:
 time.sleep(1);code,state=call('/api/tools/lookup?key='+key);assert code==200
report={'target':'own_filehelper','state':state.get('state'),'reason':state.get('reason'),'validated_server_receipt':bool(state.get('server_id'))and state.get('state')=='accepted','group_test_sends':0,'automatic_resends':0,'phone_quote_rendering_verified':False}
(workspace/'research/evidence/quote-reply-050.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(report,ensure_ascii=False))
