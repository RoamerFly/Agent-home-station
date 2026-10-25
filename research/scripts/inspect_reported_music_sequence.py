"""Read-only tool/delivery evidence for the owner's screenshot."""
import json,pathlib
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024');workspace=pathlib.Path(__file__).resolve().parents[2]
events=json.loads((root/'admin/activity.json').read_text())
received=[e for e in events if e.get('thread_id')=='43929704480@chatroom'and e.get('event')=='message_received'and any(x in e.get('content','').lower()for x in ['轻快','traveling','娱乐兴趣','设的兴趣','全是英文'])]
traces={e['message_id']for e in received};selected=[]
for e in events:
    if e.get('message_id')in traces:
        selected.append({k:e.get(k)for k in ['time','event','message_id','status','reason','content','tool','query','target_url','bytes','send_id']})
report={'read_only':True,'events':selected}
(workspace/'research/evidence/reported-music-sequence.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(report,ensure_ascii=False))
