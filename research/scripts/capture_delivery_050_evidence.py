"""Sanitized read-only receipt/queue evidence; no chat bodies or identifiers exported."""
import json,pathlib,collections
r=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024');w=pathlib.Path(__file__).resolve().parents[2]
a=json.loads((r/'admin/activity.json').read_text());pics=[x for x in a if x.get('content')=='[收到图片]']
checks=[]
for p in pics:
 rows=[x for x in a if x.get('message_id')==p['message_id']and x.get('thread_id')==p.get('thread_id')]
 checks.append({'source_kind':p.get('source_kind'),'is_filehelper':p.get('thread_id')=='filehelper','received':True,'analysis_ready':any(x['event']=='media_understood'for x in rows),'decision_or_reply_recorded':any(x['event']in['message_sent','character_decision']for x in rows),'reply_accepted':any(x['event']=='message_sent'and x.get('status')=='accepted'for x in rows),'recovered_after_restart':sum(x['event']=='message_received'for x in rows)>1})
queue_path=r/'bot-state/messages-v2.json'
if not queue_path.exists():queue_path=r/'admin/bot-state/messages-v2.json'
q=json.loads(queue_path.read_text())
report={'version':'0.5.0-local','received_picture_checks':checks,'queue_phase_counts':dict(collections.Counter(x['phase']for x in q['inbox'])),'delivery_phase_counts':dict(collections.Counter(x['state']for x in q['deliveries'])),'stored_keys_exported':False,'old_failure_messages_replayed':False,'docker_exported':False}
(w/'research/evidence/delivery-050-live.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8');print(json.dumps(report,ensure_ascii=False))
