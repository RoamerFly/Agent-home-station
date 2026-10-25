"""Read-only recent YuE failure audit. No credentials or request bodies printed."""
import json, urllib.request, urllib.error
from pathlib import Path
root = Path('/home/fly/.local/share/wechat-bot-native-research-20261024/admin')
rows = json.loads((root/'activity.json').read_text())
room = '50583054216@chatroom'
selected = [r for r in rows if r.get('thread_id') == room and
            (r.get('category') == 'received' or 'music_local' in r.get('tool', '') or
             'YuE' in r.get('reason', ''))][-16:]
print(json.dumps({'recent': [{k: r.get(k) for k in
    ('time','event','tool','content','reason','status')} for r in selected]}, ensure_ascii=False, indent=2))
cfg = json.loads((root/'speech-config.json').read_text())
url = cfg.get('yue_endpoint', '')
token = cfg.get('yue_token', '')
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
try:
    req = urllib.request.Request(url.rstrip('/')+'/health', headers={'Authorization':'Bearer '+token})
    with opener.open(req, timeout=5) as r:
        print(json.dumps({'configured_yue_http':r.status,'health':json.load(r)}))
except Exception as e:
    print(json.dumps({'configured_yue_error':type(e).__name__}))
