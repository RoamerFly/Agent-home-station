"""Read last two surf sessions and decisions without exposing credentials."""
import json
from pathlib import Path
root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024/admin')
h=json.loads((root/'surf-history.json').read_text());sessions=h.get('sessions',[])[-2:]
rows=json.loads((root/'activity.json').read_text())
starts=[r for r in rows if r.get('event')=='surf_started'][-2:];ids={r.get('message_id') for r in starts}
events=[{k:r.get(k) for k in ('time','event','message_id','source','tool','query','target_url','content','reason','status','tool_code')} for r in rows if r.get('message_id') in ids]
print(json.dumps({'sessions':sessions,'events':events},ensure_ascii=False,indent=2))
