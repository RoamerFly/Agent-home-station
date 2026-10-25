"""One controlled recheck after human confirmed original song was not received.
Not an automatic retry policy; never replay the old uncertain job.
"""
import pathlib,http.cookiejar,urllib.request,urllib.error,json,time,sqlite3,argparse
parser=argparse.ArgumentParser();parser.add_argument('--format-recheck',action='store_true');args=parser.parse_args()
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024');workspace=pathlib.Path(__file__).resolve().parents[2]
base='http://127.0.0.1:8081';csrf='';client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
def call(path,value=None):
 headers={'Origin':base,'X-CSRF-Token':csrf};body=None
 if value is not None:body=json.dumps(value).encode();headers['Content-Type']='application/json'
 try:
  with client.open(urllib.request.Request(base+path,data=body,headers=headers),timeout=120)as r:return r.status,json.load(r)
 except urllib.error.HTTPError as e:return e.code,json.load(e)
csrf=call('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})[1]['csrf']
t=json.loads((root/'admin/tool-state.json').read_text());old=t['sends']['music-060-own-filehelper-20261024']['status']
c=sqlite3.connect('file:'+str(root/'accounts/background-test-20261024/agent/control.sqlite')+'?mode=ro',uri=True)
d=json.loads(c.execute('select data_json from media_jobs where id=?',(old['id'],)).fetchone()[0])
key='music-060-format-fixed-check3'if args.format_recheck else'music-060-confirmed-not-delivered-check2'
code,status=call('/api/tools/lookup?key='+key)
if code==404:
 code,status=call('/api/tools/send',{'thread_id':'filehelper','kind':'voice','source_id':d['sourceId'],'idempotency_key':key})
assert code in(200,202),(code,status.get('error'))
until=time.monotonic()+180
while status['state']in['queued','running','sending','submitting']and time.monotonic()<until:
 time.sleep(1);code,status=call('/api/tools/lookup?key='+key);assert code==200
d=json.loads(c.execute('select data_json from media_jobs where id=?',(status['id'],)).fetchone()[0]);chunks=d.get('result',{}).get('chunks',[])
report={'track':'Happy Alley','old_job_phone_received':False,'reused_original_download':True,'controlled_recheck_after_user_confirmation':True,'group_sends':0,'automatic_resends':0,'state':status['state'],'reason':status.get('reason'),'voice_chunks':len(chunks),'accepted_chunks':sum(x.get('state')=='accepted'for x in chunks),'all_chunk_receipts_present':bool(chunks)and all(x.get('serverMessageId')for x in chunks),'phone_playback_verified':False}
if args.format_recheck:report.update({'voice_format_fixed':True,'previous_music_notes_received_but_unplayable':True})
(workspace/('research/evidence/song-voice-060-format-fixed.json'if args.format_recheck else'research/evidence/song-voice-060-recheck.json')).write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8');print(json.dumps(report,ensure_ascii=False),flush=True)
