"""Read the specific reported song's task state; no credentials or external sends."""
import json
from pathlib import Path
root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024/admin')
rows=json.loads((root/'activity.json').read_text())
room='45005229594@chatroom'
received=[r for r in rows if r.get('thread_id')==room and r.get('category')=='received' and '忘情' in r.get('content','')]
ids={r.get('message_id') for r in received}
relevant=[{k:r.get(k) for k in ('time','event','tool','content','reason','status','message_id')} for r in rows if r.get('thread_id')==room and (r.get('message_id') in ids or r.get('tool') in ('music_local_start','music_local_status'))][-35:]
data=json.loads((root/'agent-state.json').read_text())
ops=[]
for op in data['operations'].values():
    if op.get('thread')!=room or op.get('tool') not in ('music_local_start','music_local_status'):continue
    item={k:op.get(k) for k in ('id','tool','state','at','result')}
    args=op.get('arguments',{});item['arguments']={k:v for k,v in args.items() if k in ('title','duration_seconds','kind','job_id','reference_asset_id')}
    ops.append(item)
tasks=[{k:t.get(k) for k in ('id','title','state','checkpoint','music_job','music_kind','music_prepared','music_deadline','requested_text','last_result','delivery','origin_trace')} for t in data['tasks'].values() if t.get('thread')==room]
jobs=json.loads((Path(__file__).resolve().parents[2]/'secrets/yue-jobs/jobs.json').read_text())
job_ids={t.get('music_job') for t in tasks}
print(json.dumps({'received':received[-3:],'activities':relevant,'operations':ops[-8:],'tasks':tasks[-8:],'bridge_jobs':[{k:v for k,v in j.items() if k not in ('key','file')} for j in jobs.values() if j['id'] in job_ids]},ensure_ascii=False,indent=2))
