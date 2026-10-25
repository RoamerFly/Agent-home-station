"""Bind the two verified old group voice jobs to structured memory; no sends."""
import http.cookiejar,json,pathlib,urllib.request,sqlite3,time
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
workspace=pathlib.Path(__file__).resolve().parents[2]
base='http://127.0.0.1:8081'
jar=http.cookiejar.CookieJar();client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(jar))
def call(route,data=None,csrf=''):
    headers={'Origin':base,'Content-Type':'application/json'}
    if csrf:headers['X-CSRF-Token']=csrf
    req=urllib.request.Request(base+route,data=None if data is None else json.dumps(data).encode(),headers=headers)
    with client.open(req,timeout=20)as response:return json.load(response)
csrf=call('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})['csrf']
activity=json.loads((root/'admin/activity.json').read_text())
db=sqlite3.connect('file:'+str(root/'accounts/background-test-20261024/agent/control.sqlite')+'?mode=ro',uri=True)
results=[]
for job in ['job_af5d84ec-41b3-4090-aafe-408cd40b6a69','job_ffc3480e-68b9-4d28-b945-d0b60f046889']:
    row=db.execute('select data_json,state from media_jobs where id=?',(job,)).fetchone();assert row
    native=json.loads(row[0]);assert native['threadId']=='50583054216@chatroom'and row[1]=='accepted'
    traces=[x['message_id']for x in activity if x.get('send_id')==job and x.get('message_id')]
    if not traces:
        results.append({'job_id':job,'adopted':False,'reason':'no_verified_incoming_trace'});continue
    result=call('/api/media/recover',{'job_id':job,'trace':traces[0],'kind':'voice'},csrf)
    results.append({'job_id':job,**result})
time.sleep(4)
scopes=call('/api/companion/state').get('scopes',[])
scope=next(x for x in scopes if x['thread']=='50583054216@chatroom')
memory=call('/api/companion/memory/'+scope['id'])
media=[x['media']for x in memory.get('messages',[])if x.get('media')and x['media'].get('job_id')in [r['job_id']for r in results]]
report={'adoptions':results,'memory_media':media,'new_sends_submitted':0}
(workspace/'research/evidence/voice-memory-backfill.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps({'adopted':sum(x.get('adopted',False)for x in results),'memory_entries':len(media),'states':[x['state']for x in media],'new_sends_submitted':0}))
