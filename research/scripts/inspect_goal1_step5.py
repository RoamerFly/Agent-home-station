"""Extract bounded historical web diagnostics; never copy credentials or chats."""
import json,pathlib
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
workspace=pathlib.Path(__file__).resolve().parents[2]
items=json.loads((root/'admin/activity.json').read_text())
selected=[]
for item in items:
    if item.get('event')=='web_activity' and (item.get('query')=='Happy Alley' or 'Happy%20Alley.mp3' in item.get('target_url','')):
        selected.append({key:item[key] for key in ('time','tool','status','reason','duration_ms','bytes','media_count','origin') if key in item})
traces={item.get('message_id') for item in items if 'Newer Wave' in item.get('content','') and item.get('message_id')}
related=[{key:item[key] for key in ('time','tool','status','reason','duration_ms','bytes','media_count','origin') if key in item} for item in items if item.get('event')=='web_activity' and item.get('message_id') in traces]
report={'legacy_web_activities':selected,'legacy_newer_wave_reply_found':bool(traces),'legacy_newer_wave_tool_events':related,'legacy_transport_subtype':'not_recorded_cannot_reconstruct','legacy_attempts_not_recorded':True}
(workspace/'research/evidence/goal1-step5-history.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(report,ensure_ascii=False))
