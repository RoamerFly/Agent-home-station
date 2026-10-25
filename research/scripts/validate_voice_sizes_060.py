"""Bounded diagnostic: at most one 1s, 15s and 40s tone to own filehelper.
Stable keys prevent replay; stop immediately on unverified submission.
"""
import pathlib,http.cookiejar,urllib.request,urllib.error,json,time,subprocess,uuid,argparse
parser=argparse.ArgumentParser();group=parser.add_mutually_exclusive_group();group.add_argument('--range-16k',action='store_true');group.add_argument('--format-recheck',action='store_true');args=parser.parse_args()
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024');workspace=pathlib.Path(__file__).resolve().parents[2]
base='http://127.0.0.1:8081';csrf='';client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
def call(path,value=None,raw=None,typ='application/json'):
 headers={'Origin':base,'X-CSRF-Token':csrf}
 if value is not None:raw=json.dumps(value).encode()
 if raw is not None:headers['Content-Type']=typ
 try:
  with client.open(urllib.request.Request(base+path,data=raw,headers=headers),timeout=120)as r:return r.status,json.load(r)
 except urllib.error.HTTPError as e:return e.code,json.load(e)
csrf=call('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})[1]['csrf']
report={'recipient':'own_filehelper','synthetic_diagnostic_only':True,'group_sends':0,'automatic_resends':0,'checks':[]}
folder=root/'admin/validation-media';folder.mkdir(mode=0o700,exist_ok=True)
for seconds in ([15]if args.range_16k or args.format_recheck else[1,15,40]):
 key='voice-size-060-'+str(seconds)+'s-'+('format-recheck'if args.format_recheck else'16k-recheck'if args.range_16k else'20261024')
 code,status=call('/api/tools/lookup?key='+key)
 if code==404:
  source=folder/('tone-'+str(seconds)+'s.wav')
  subprocess.run(['ffmpeg','-nostdin','-v','error','-y','-f','lavfi','-i','sine=frequency=440:duration='+str(seconds),'-ar','16000','-ac','1',str(source)],check=True);source.chmod(0o600)
  boundary='voice-size-'+uuid.uuid4().hex
  body=('--'+boundary+'\r\nContent-Disposition: form-data; name="kind"\r\n\r\nvoice\r\n--'+boundary+'\r\nContent-Disposition: form-data; name="file"; filename="tone.wav"\r\nContent-Type: audio/wav\r\n\r\n').encode()+source.read_bytes()+('\r\n--'+boundary+'--\r\n').encode()
  code,upload=call('/api/tools/upload',raw=body,typ='multipart/form-data; boundary='+boundary);assert code==201,(code,upload.get('error'))
  code,status=call('/api/tools/send',{'thread_id':'filehelper','kind':'voice','source_id':upload['id'],'idempotency_key':key})
 assert code in(200,202)
 until=time.monotonic()+150
 while status['state']in['queued','running','sending','submitting']and time.monotonic()<until:
  time.sleep(1);code,status=call('/api/tools/lookup?key='+key);assert code==200
 result={'seconds':seconds,'state':status['state'],'reason':status.get('reason'),'receipt':bool(status.get('server_id'))};report['checks'].append(result);print(json.dumps(result),flush=True)
 if status['state']!='accepted':break
(workspace/('research/evidence/voice-format-recheck.json'if args.format_recheck else'research/evidence/voice-range-16k-recheck.json'if args.range_16k else'research/evidence/voice-sizes-060.json')).write_text(json.dumps(report,indent=2),encoding='utf8')
