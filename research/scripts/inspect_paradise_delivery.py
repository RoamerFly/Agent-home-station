"""Inspect only the reported Paradise Found task; do not submit anything."""
import datetime,json,pathlib,sqlite3
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
workspace=pathlib.Path(__file__).resolve().parents[2]
items=json.loads((root/'admin/activity.json').read_text())
traces={x.get('message_id') for x in items if 'Paradise Found' in x.get('content','') and x.get('message_id')}
selected=[x for x in items if x.get('message_id') in traces]
jobs=set(x.get('send_id') for x in selected if x.get('send_id'))
db=sqlite3.connect('file:'+str(root/'accounts/background-test-20261024/agent/control.sqlite')+'?mode=ro',uri=True)
details=[]
for job in jobs:
    row=db.execute('select id,state,phase,error_code,data_json,created_at from media_jobs where id=?',(job,)).fetchone()
    if not row:continue
    data=json.loads(row[4]);result=data.get('result',{})
    chunks=[{k:c[k] for k in ('index','state','durationMs','pacingMs','submittedAtMs','acceptedAtMs','prefixRetries') if k in c}|{'has_receipt':bool(c.get('serverMessageId'))}for c in result.get('chunks',[])]
    details.append({'id':row[0],'state':row[1],'phase':row[2],'reason':row[3],'created':row[5],'sourceId':data.get('sourceId'),'kind':data.get('kind'),'threadId':data.get('threadId'),'chunks':chunks,'result_fields':list(result),'diagnostics':{k:v for k,v in result.items() if k in ('voiceFailureStage','voiceTransportKind','voiceFailedOffset')}})
audit=json.loads((root/'admin/audit.json').read_text())
updates=[{k:x[k] for k in ('time','event')if k in x} for x in audit if '2026-10-24T18:46'<=x.get('time','')<'2026-10-24T18:49']
queue=[dict(zip(('id','kind','state','phase','attempts','reason'),row)) for row in db.execute("select id,kind,state,phase,attempts,error_code from media_jobs where state in ('queued','running')")]
report={'activity':[{k:x[k] for k in ('time','event','status','reason','content','send_id','message_id','origin') if k in x}for x in selected],'jobs':details,'web_update_events':updates,'active_native_jobs':queue}
(workspace/'research/evidence/paradise-delivery-history.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(report,ensure_ascii=False))
