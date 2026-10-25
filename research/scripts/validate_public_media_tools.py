"""Local authenticated tool checks. Downloads/stages public fixtures, never sends WeChat."""
import http.cookiejar,json,urllib.request,urllib.error,pathlib,hashlib,sqlite3
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
workspace=pathlib.Path(__file__).resolve().parents[2]
base='http://127.0.0.1:8081'
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
csrf=''
def call(path,value=None):
 headers={'Origin':base}
 if csrf:headers['X-CSRF-Token']=csrf
 data=None
 if value is not None:data=json.dumps(value).encode();headers['Content-Type']='application/json'
 try:
  with client.open(urllib.request.Request(base+path,data=data,headers=headers),timeout=100)as r:return r.status,json.load(r)
 except urllib.error.HTTPError as e:return e.code,json.load(e)
csrf=call('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})[1]['csrf']
config=root/'admin/config.json';before=hashlib.sha256(config.read_bytes()).hexdigest()
c=sqlite3.connect('file:'+str(root/'accounts/background-test-20261024/agent/control.sqlite')+'?mode=ro',uri=True)
send_count=c.execute('select count(*) from outbox').fetchone()[0]
report={'checks':{},'wechat_sends_in_this_check':0,'docker_export':False}
for name,args in [
 ('fetch_missing_url',{'query':'deepseek'}),
 ('fetch_public_page',{'url':'https://example.com/'}),
 ('fetch_media_candidates',{'url':'https://www.w3schools.com/html/html5_video.asp'}),
 ('download_audio',{'url':'https://www.w3schools.com/html/horse.mp3','kind':'voice'}),
 ('download_video',{'url':'https://www.w3schools.com/html/mov_bbb.mp4','kind':'video'}),
]:
 tool='web_media_download'if name.startswith('download')else'web_fetch'
 code,data=call('/api/mcp/test',{'name':tool,'arguments':args})
 check={'http_status':code,'ok':code==200}
 if name=='fetch_missing_url':check={'http_status':code,'ok':code==502 and'url'in data.get('error',''),'diagnostic':data.get('error')}
 elif code!=200:check['diagnostic']=data.get('error')
 elif name=='fetch_public_page':check['has_source_and_text']='Source:'in data['result']and'Example Domain'in data['result']
 elif name=='fetch_media_candidates':check['has_media_candidates']='Public media candidates'in data['result']
 else:
  output=json.loads(data['result']);media=output.get('media',[])
  check['staged_kind']=media[0].get('kind')if media else None
  check['real_native_source']=bool(media and media[0].get('id'))
  check['ok']=bool(check['real_native_source']and check['staged_kind']==args['kind'])
 report['checks'][name]=check;print(name,json.dumps(check,ensure_ascii=False),flush=True)
for kind,url in [('voice','https://www.w3schools.com/html/horse.mp3'),('video','https://www.w3schools.com/html/mov_bbb.mp4')]:
 text=('请把这个现有的音频文件作为语音分享给我：'if kind=='voice'else'请把这个现有的视频文件发给我：')+url+' 。请直接用实际附件回复，配一句自然的文字，不要只给链接。'
 code,data=call('/api/companion/preview',{'mode':'direct','text':text})
 decision=data.get('decision',{})
 check={'http_status':code,'action':decision.get('action'),'has_real_media_ids':bool(decision.get('media_ids')),'dry_run':data.get('dry_run')}
 check['ok']=code==200 and decision.get('action')=='reply'and check['has_real_media_ids']and check['dry_run']
 check['no_tool_jargon']=not any(word in' '.join(decision.get('messages',[]))for word in ['MCP','web_media_download','source_id'])
 report['checks']['model_'+kind]=check;print('model_'+kind,json.dumps(check,ensure_ascii=False),flush=True)
status=call('/api/status')[1]
report.update({'version':status.get('version'),'bot_state':status['bot']['state'],'wechat_authenticated':status['native']['authenticated'],'configuration_unchanged':before==hashlib.sha256(config.read_bytes()).hexdigest(),'native_outbox_count_unchanged':send_count==c.execute('select count(*) from outbox').fetchone()[0]})
incident=c.execute('select state,error_code,server_id from outbox where id=?',('32f4edfa-87e7-49a1-8d43-cd459c0f76b4',)).fetchone()
report['reported_incident']={'state':incident[0],'reason':incident[1],'has_server_receipt':bool(incident[2]),'replayed':False}
(workspace/'research/evidence/public-media-tools-check.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
assert all(x['ok']for x in report['checks'].values()),'Some live tool checks failed; inspect report'
assert report['configuration_unchanged']
