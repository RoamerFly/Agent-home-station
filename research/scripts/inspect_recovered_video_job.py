"""Read the exact recovered video job; do not retry or cancel it."""
import json,pathlib,sqlite3,subprocess
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
trace=json.loads((root/'admin/reported-video-request-recovery.json').read_text())['trace']
q=json.loads((root/'bot-state/messages-v2.json').read_text());jobs=[x for x in q.get('media',[])if x.get('trace')==trace]
db=sqlite3.connect('file:'+str(root/'accounts/background-test-20261024/agent/control.sqlite')+'?mode=ro',uri=True)
report=[]
for item in jobs:
    row=db.execute('select state,phase,error_code,data_json,updated_at from media_jobs where id=?',(item['id'],)).fetchone()
    if row:
        data=json.loads(row[3]);prepared=data.get('result',{}).get('videoPreparation',{});report.append({'id':item['id'],'native_state':row[0],'native_phase':row[1],'native_reason':row[2],'has_server_receipt':bool(data.get('serverMessageId')),'updated':row[4],'web_state':item['state'],'result_fields':list(data.get('result',{})),'preparation':{k:v for k,v in prepared.items()if k in ['durationMs','width','height','byteLength','outputBytes','quality','codec','videoBytes']}})
print(json.dumps({'jobs':report,'web_active':subprocess.run(['systemctl','--user','is-active','--quiet','wechat-bot-web.service']).returncode==0}))
