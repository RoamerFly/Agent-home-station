"""Download one full publicly offered track and send it once to own filehelper.
Persistent key and lookup prevent duplicate sends on rerun. No group receives it.
"""
import pathlib,http.cookiejar,urllib.request,urllib.error,json,time,sqlite3
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024');workspace=pathlib.Path(__file__).resolve().parents[2]
base='http://127.0.0.1:8081';csrf='';client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
def call(path,value=None):
 headers={'Origin':base,'X-CSRF-Token':csrf};data=None
 if value is not None:data=json.dumps(value).encode();headers['Content-Type']='application/json'
 try:
  with client.open(urllib.request.Request(base+path,data=data,headers=headers),timeout=400)as r:return r.status,json.load(r)
 except urllib.error.HTTPError as e:return e.code,json.load(e)
csrf=call('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})[1]['csrf']
key='music-060-own-filehelper-20261024'
code,status=call('/api/tools/lookup?key='+key)
if code==404:
 code,result=call('/api/mcp/test',{'name':'web_audio_search','arguments':{'query':'Happy Alley'}});assert code==200,(code,result.get('error'))
 candidates=json.loads(result['result'])['candidates'];song=next((x for x in candidates if x.get('title')=='Happy Alley'),None);assert song,'Exact test track not returned'
 code,result=call('/api/mcp/test',{'name':'web_media_download','arguments':{'url':song['url'],'kind':'voice'}});assert code==200,(code,result.get('error'))
 source=json.loads(result['result'])['media'][0]['id']
 credit_key=key+'-credit';credit_code,_=call('/api/tools/lookup?key='+credit_key)
 if credit_code==404:call('/api/tools/send',{'thread_id':'filehelper','kind':'text','text':'[测试音乐] Happy Alley — Kevin MacLeod (incompetech.com)，CC BY 4.0。来源：https://incompetech.com/music/royalty-free/index.html?isrc=USUAN1100482','idempotency_key':credit_key})
 code,status=call('/api/tools/send',{'thread_id':'filehelper','kind':'voice','source_id':source,'idempotency_key':key})
assert code in(200,202),(code,status.get('error'))
deadline=time.monotonic()+180
while status['state']in['queued','running','sending','submitting']and time.monotonic()<deadline:
 time.sleep(1);code,status=call('/api/tools/lookup?key='+key);assert code==200
c=sqlite3.connect('file:'+str(root/'accounts/background-test-20261024/agent/control.sqlite')+'?mode=ro',uri=True)
row=c.execute('select data_json from media_jobs where id=?',(status.get('id'),)).fetchone();chunks=json.loads(row[0]).get('result',{}).get('chunks',[])if row else[]
report={'track':'Happy Alley','artist':'Kevin MacLeod','source':'https://incompetech.com/music/royalty-free/index.html?isrc=USUAN1100482','full_track_downloaded':True,'target':'own_filehelper','state':status['state'],'reason':status.get('reason'),'voice_chunks':len(chunks),'all_chunk_receipts_present':bool(chunks)and all(x.get('serverMessageId')for x in chunks),'validated_server_receipt':bool(status.get('server_id')),'automatic_resends':0,'group_sends':0,'phone_playback_verified':False}
(workspace/'research/evidence/song-voice-060.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8');print(json.dumps(report,ensure_ascii=False),flush=True)
assert status['state']=='accepted'and report['all_chunk_receipts_present'],'Song voice not accepted; never auto-resend'
