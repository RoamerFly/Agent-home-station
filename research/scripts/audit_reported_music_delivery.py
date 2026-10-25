"""Read only the reported music task and receipt metadata; never print keys."""
import json,subprocess
from pathlib import Path
root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024');workspace=Path(__file__).resolve().parents[2]
rows=json.loads((root/'admin/activity.json').read_text())
selected=[{k:r.get(k) for k in ['time','event','message_id','send_id','content','reason','status']} for r in rows if r.get('thread_id')=='50583054216@chatroom' and '2026-10-25T06:11'<=r.get('time','')<'2026-10-25T06:18']
ops=json.loads((root/'admin/agent-state.json').read_text())['operations']
jobs_root=workspace/'secrets/yue-jobs';jobs=json.loads((jobs_root/'jobs.json').read_text())
generation=[]
for op in ops.values():
    if op.get('thread')!='50583054216@chatroom' or op.get('tool')!='music_local_start' or not ('2026-10-25T06:11'<=op.get('at','')<'2026-10-25T06:13'):continue
    args=op['arguments'];result=json.loads(op['result']);job=jobs.get(result.get('id'),{})
    entry={'kind':args.get('kind'),'duration_parameter_present':'duration_seconds' in args,'state':job.get('state')}
    if job.get('file'):
        source=(jobs_root/job['file']).resolve();assert source.parent==jobs_root.resolve()
        entry['actual_seconds']=float(subprocess.check_output(['ffprobe','-v','error','-show_entries','format=duration','-of','default=nw=1:nk=1',str(source)]))
    generation.append(entry)
report={'generation':generation,'events':selected,'read_only':True,'new_sends':0}
(workspace/'research/evidence/reported-music-voice-diagnosis.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps({'generation':generation,'event_count':len(selected),'read_only':True,'new_sends':0},ensure_ascii=False))
