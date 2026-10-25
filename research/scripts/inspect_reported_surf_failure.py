"""Read only the reported surf events; omit private chats and credentials."""
import json,pathlib
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024');workspace=pathlib.Path(__file__).resolve().parents[2]
events=json.loads((root/'admin/activity.json').read_text())
window=[];quantum=[]
for event in events:
    stamp=event.get('time','')
    if stamp.startswith('2026-10-25T01:53:') or ('Cloud%20Dancer.mp3'in event.get('target_url','')and event.get('event')=='web_activity'):
        window.append({k:event.get(k)for k in ['time','event','origin','tool','query','target_url','status','reason','tool_requests','tool_retries','bytes']})
    if event.get('event')=='message_sent'and event.get('status')=='accepted'and '三比特'in event.get('content',''):
        quantum.append({'time':stamp,'trace_is_surf':event.get('message_id','').startswith('surf-'),'contains_quantum_topic':True})
report={'reported_window':window,'accepted_quantum_shares_in_retained_journal':quantum,'historical_logs_rewritten':False,'wechat_sends':0}
(workspace/'research/evidence/reported-surf-failure-inspection.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(report,ensure_ascii=False))
