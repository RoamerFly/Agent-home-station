import json,pathlib,sqlite3
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
db=sqlite3.connect('file:'+str(root/'accounts/background-test-20261024/agent/control.sqlite')+'?mode=ro',uri=True)
original=db.execute('select data_json from media_jobs where id=?',('job_af5d84ec-41b3-4090-aafe-408cd40b6a69',)).fetchone()
old=json.loads(original[0]);old_hash=db.execute('select sha256 from media_sources where id=?',(old['sourceId'],)).fetchone()[0]
out=[]
for row in db.execute("select id,state,phase,attempts,data_json,updated_at from media_jobs where state in ('queued','running') or id='job_ffc3480e-68b9-4d28-b945-d0b60f046889'"):
    data=json.loads(row[4]);chunks=data.get('result',{}).get('chunks',[])
    source=db.execute('select sha256 from media_sources where id=?',(data.get('sourceId',''),)).fetchone()
    out.append({'id':row[0],'state':row[1],'phase':row[2],'attempts':row[3],'updated':row[5],'same_reported_thread':data.get('threadId')==old['threadId'],'same_reported_source':bool(source and source[0]==old_hash),'accepted_chunks':sum(x['state']=='accepted'for x in chunks),'total_chunks':len(chunks)})
print(json.dumps(out))
