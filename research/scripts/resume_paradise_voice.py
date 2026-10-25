"""Resume the user's reported, cancelled-before-submission job once; no text sends."""
import datetime,json,pathlib,time,urllib.request
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
workspace=pathlib.Path(__file__).resolve().parents[2]
history=json.loads((workspace/'research/evidence/paradise-delivery-history.json').read_text())
assert len(history['jobs'])==1
original=history['jobs'][0]
c=json.loads((root/'admin/config.json').read_text())
assert c['enabled'] and c['companion']['permissions']['send_voice']
assert any(x['id']==original['threadId'] and x['name']=='机器人测试群' for x in c['group_rules'])
client=urllib.request.build_opener(urllib.request.ProxyHandler({}))
base='http://127.0.0.1:6174/v1/accounts/background-test-20261024'
token=(root/'api-token').read_text().strip()
def call(method,path,body=None):
    data=None if body is None else json.dumps(body).encode()
    req=urllib.request.Request(base+path,data=data,method=method,headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
    with client.open(req,timeout=15) as response:return json.load(response)
job=call('GET','/media-jobs/'+original['id']) if False else call('GET','/sends/'+original['id'])
resumed=False
if job['state'] not in ('accepted','queued','running'):
    assert job['state']=='cancelled' and job['phase']=='pacing' and job['retryable'] and job['attempts']<3
    assert job.get('sourceId')==original['sourceId'] and job.get('threadId')==original['threadId']
    chunks=job.get('result',{}).get('chunks',[])
    assert len(chunks)==4 and all(x['state']=='prepared' and not x.get('submittedAtMs') and not x.get('serverMessageId') for x in chunks)
    created=datetime.datetime.fromisoformat(job['createdAt'].replace('Z','+00:00'))
    assert (datetime.datetime.now(datetime.timezone.utc)-created).total_seconds()>=120
    job=call('POST','/jobs/'+original['id']+'/retry',{})
    resumed=True
print(json.dumps({'same_original_job':True,'new_resume_request_this_run':resumed,'original_state':'cancelled_before_first_submission','target':'user_reported_robot_test_group','text_messages_submitted':0,'current_state':job['state']},ensure_ascii=False),flush=True)
deadline=time.monotonic()+12*60
last=None
while time.monotonic()<deadline:
    job=call('GET','/sends/'+original['id'])
    chunks=job.get('result',{}).get('chunks',[])
    summary={'state':job['state'],'phase':job['phase'],'reason':job.get('reason'),'accepted_chunks':sum(x['state']=='accepted' and bool(x.get('serverMessageId'))for x in chunks),'total_chunks':len(chunks)}
    if summary!=last:print(json.dumps(summary,ensure_ascii=False),flush=True);last=summary
    if job['state'] not in ('queued','running'):break
    time.sleep(2)
report={'same_original_job':True,'new_resume_request_this_run':resumed,'original_id':original['id'],'source_id':original['sourceId'],'target':'user_reported_robot_test_group','state':job['state'],'phase':job['phase'],'reason':job.get('reason'),'chunks':[{k:x[k] for k in ('index','state','durationMs','pacingMs','submittedAtMs','acceptedAtMs','prefixRetries')if k in x}|{'has_receipt':bool(x.get('serverMessageId'))}for x in chunks],'text_messages_submitted':0,'phone_playback_verified':False}
(workspace/'research/evidence/paradise-delivery-resumed.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(report,ensure_ascii=False),flush=True)
