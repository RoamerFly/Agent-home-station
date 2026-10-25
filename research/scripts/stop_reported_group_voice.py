"""Honor the user's explicit switch-to-file request; cancel pending voice only."""
import json,pathlib,sqlite3,urllib.request
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
workspace=pathlib.Path(__file__).resolve().parents[2]
history=json.loads((workspace/'research/evidence/paradise-delivery-resumed.json').read_text())
db=sqlite3.connect('file:'+str(root/'accounts/background-test-20261024/agent/control.sqlite')+'?mode=ro',uri=True)
row=db.execute('select data_json from media_jobs where id=?',(history['original_id'],)).fetchone();thread=json.loads(row[0])['threadId']
c=json.loads((root/'admin/config.json').read_text())
assert any(x['id']==thread and x['name']=='机器人测试群'for x in c['group_rules'])
client=urllib.request.build_opener(urllib.request.ProxyHandler({}));token=(root/'api-token').read_text().strip()
cancelled=[]
for job_id,data in db.execute("select id,data_json from media_jobs where state in ('queued','running')"):
    job=json.loads(data);parts=job.get('result',{}).get('chunks',[])
    if job.get('threadId')!=thread or not parts or not all('durationMs'in x for x in parts):continue
    req=urllib.request.Request('http://127.0.0.1:6174/v1/accounts/background-test-20261024/jobs/'+job_id+'/cancel',data=b'{}',method='POST',headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
    with client.open(req,timeout=10) as response:assert response.status==200
    cancelled.append(job_id)
report={'reason':'user_requested_file_instead_of_voice','pending_voice_cancel_requests':cancelled,'new_send_requests':0,'already_accepted_messages_recalled':False}
(workspace/'research/evidence/voice-switch-stop.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(report,ensure_ascii=False))
