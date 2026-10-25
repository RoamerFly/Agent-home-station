"""Print only media linkage and reported request diagnostics, never credentials."""
import json
from pathlib import Path
root=Path('/home/fly/.local/share/wechat-bot-native-research-20261024/admin')
data=json.loads((root/'companion/memory.json').read_text())
report=[]
for scope in data['scopes'].values():
    turns=scope['messages']
    matches=[i for i,t in enumerate(turns) if '给我转发一下原视频' in t.get('text','')]
    for i in matches:
        prior=turns[:i]
        media=[{'time':t.get('time'),'role':t['role'],'media':{k:t.get('media',{}).get(k) for k in ['kind','title','asset_id','url','state']}} for t in prior if t.get('media')][-5:]
        report.append({'scope':scope['thread'],'request_time':turns[i].get('time'),'media_before_request':media,'stored_messages':len(turns)})
assets=json.loads((root/'media-assets.json').read_text())
for entry in report:
    ids={x['media']['asset_id'] for x in entry['media_before_request']}
    entry['linked_sources']=[{k:a.get(k) for k in ['id','title','source_url','updated']} for a in assets if a['id'] in ids]
workspace=Path(__file__).resolve().parents[2]
(workspace/'research/evidence/media-context-diagnosis.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(report,ensure_ascii=False,indent=2))
