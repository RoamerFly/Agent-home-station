"""One controlled text/audio/video check to this account's own filehelper.
Stable keys and lookup-before-create prevent resending on script reruns.
No group receives a test and no historical bot reply is replayed.
"""
import http.cookiejar,json,urllib.request,urllib.error,urllib.parse,pathlib,time
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024');workspace=pathlib.Path(__file__).resolve().parents[2]
base='http://127.0.0.1:8081';csrf=''
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
def call(path,value=None):
 headers={'Origin':base,'X-CSRF-Token':csrf};data=None
 if value is not None:data=json.dumps(value).encode();headers['Content-Type']='application/json'
 try:
  with client.open(urllib.request.Request(base+path,data=data,headers=headers),timeout=90)as r:return r.status,json.load(r)
 except urllib.error.HTTPError as e:return e.code,json.load(e)
csrf=call('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})[1]['csrf']
report={'recipient':'own_filehelper','group_sends':0,'old_messages_replayed':False,'checks':{}}
for kind,url in [('text',None),('voice','https://www.w3schools.com/html/horse.mp3'),('video','https://www.w3schools.com/html/mov_bbb.mp4')]:
 key='repair-043-filehelper-'+kind+'-20261024'
 code,state=call('/api/tools/lookup?'+urllib.parse.urlencode({'key':key}))
 if kind=='video'and code==200 and state.get('state')=='failed'and state.get('phase')=='preparing'and state.get('reason')=='media_processing_failed':
  report['initial_video_attempt']={'state':'failed','phase':'preparing','reason':'media_processing_failed','submitted_to_wechat':False}
  # A new explicitly controlled test of the repaired remux path. Never reuse
  # this branch for needs_review, unknown or any post-submission failure.
  key+='-remux-check'
  code,state=call('/api/tools/lookup?'+urllib.parse.urlencode({'key':key}))
 if code==404:
  payload={'thread_id':'filehelper','kind':kind,'idempotency_key':key}
  if kind=='text':payload['text']='[测试] 新消息发送链路检查'
  else:
   code,data=call('/api/mcp/test',{'name':'web_media_download','arguments':{'url':url,'kind':kind}})
   assert code==200,(kind,code,data.get('error'))
   payload['source_id']=json.loads(data['result'])['media'][0]['id']
  code,state=call('/api/tools/send',payload)
 assert code in(200,202),(kind,code,state.get('error'))
 deadline=time.monotonic()+150
 while state.get('state')in('queued','sending','submitting','running')and time.monotonic()<deadline:
  time.sleep(1)
  code,state=call('/api/tools/sends/'+urllib.parse.quote(state['id']))
  assert code==200,(kind,code,state.get('error'))
 result={k:state.get(k)for k in['state','phase','reason']}
 result['validated_server_receipt']=bool(state.get('server_id'))and state.get('state')=='accepted'
 report['checks'][kind]=result;print(kind,json.dumps(result,ensure_ascii=False),flush=True)
 if state.get('state')!='accepted':break
(workspace/'research/evidence/repaired-self-delivery.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
assert len(report['checks'])==3 and all(x['validated_server_receipt']for x in report['checks'].values()),'Self-delivery check incomplete; never auto-resend unknown results'
