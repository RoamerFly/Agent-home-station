"""Inspect historical failures, retaining only relevant bounded diagnostics."""
import json
import pathlib
import sqlite3

root = pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
workspace = pathlib.Path(__file__).resolve().parents[2]
items = json.loads((root / 'admin/activity.json').read_text())
relevant = []
traces = set()
for item in items:
    if any(needle in item.get('content', '') for needle in ['还在加功能', '你目前可调用的mcp工具有哪些', 'generated-video.f4v']):
        relevant.append(item)
        traces.add(item.get('message_id'))
for item in items:
    if (item.get('message_id') in traces or item.get('event') == 'model_failed') and item not in relevant:
        relevant.append(item)
allowed = ['time', 'event', 'status', 'reason', 'content', 'send_id', 'message_id', 'origin']
activity = [{k: item[k] for k in allowed if k in item} for item in relevant]
db = sqlite3.connect('file:' + str(root / 'accounts/background-test-20261024/agent/control.sqlite') + '?mode=ro', uri=True)
videos = []
for row in db.execute("select j.id,j.state,j.phase,j.error_code,s.name,j.data_json,j.created_at from media_jobs j join media_sources s on s.id=json_extract(j.data_json,'$.sourceId') where j.kind='send' and (s.name like '%video%' or s.name like '%.f4v') order by j.created_at"):
    result = json.loads(row[5]).get('result', {})
    videos.append({'id': row[0], 'state': row[1], 'phase': row[2], 'reason': row[3], 'filename': row[4], 'result': result, 'created': row[6]})
report = {'activity': activity, 'video_jobs': videos}
(workspace / 'research/evidence/goal1-step1-history.json').write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding='utf8')
print(json.dumps(report, ensure_ascii=False))
