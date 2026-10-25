"""Record already-generated test output; never generate or send messages."""
import json,shutil,subprocess
from pathlib import Path
workspace=Path(__file__).resolve().parents[2]
evidence=workspace/'research/evidence'
initial=json.loads((evidence/'music-followup-live.json').read_text(encoding='utf8'))
completion=json.loads((evidence/'music-followup-completion.json').read_text(encoding='utf8'))
state=workspace/'secrets/yue-jobs'
job=json.loads((state/'jobs.json').read_text(encoding='utf8'))[initial['job_id']]
source=(state/job['file']).resolve();assert source.parent==state.resolve() and source.is_file() and job['state']=='completed'
target=evidence/'music-followup-melancholic.flac';shutil.copyfile(source,target)
metadata=json.loads(subprocess.check_output(['ffprobe','-v','error','-show_entries','format=duration,size','-of','json',str(target)]))
subprocess.run(['ffmpeg','-v','error','-i',str(target),'-f','null','-'],check=True,stdout=subprocess.DEVNULL)
subprocess.run(['ffmpeg','-v','error','-y','-i',str(target),'-c:a','libmp3lame','-b:a','192k',str(evidence/'music-followup-melancholic.mp3')],check=True)
result={'style_confirmation_started_local_model':bool(initial['registered_tasks']),'same_job_completed':job['state']=='completed','completion_selected_staged_media':bool(completion['decision']['media_ids']),'proactive_permission':False,'wechat_sends':0,'output':metadata['format'],'note':'Initial live run exposed asset ID confusion; completion was retested with the same finished job after the fix. No song regeneration in completion retest.'}
(evidence/'music-followup-acceptance.json').write_text(json.dumps(result,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(result,ensure_ascii=False,indent=2))
