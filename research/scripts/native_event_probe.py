"""Read event contract metadata without printing private message contents."""
import json
from pathlib import Path
import urllib.request
root = Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
token = (root / 'api-token').read_text().strip()
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
def get(path):
    req = urllib.request.Request('http://127.0.0.1:6174' + path, headers={'Authorization':'Bearer ' + token})
    with opener.open(req, timeout=10) as r: return json.load(r)
prefix='/v1/accounts/background-test-20261024'
status=get(prefix)
page=get(prefix + '/events?after=0&limit=200')
schemas={}
for event in page['items']:
    schemas[event['kind']] = sorted(event['data'].keys())
report={'authenticated':status.get('authenticated'),'account_status_keys':sorted(status.keys()),'event_count':len(page['items']),'next_cursor':page['nextCursor'],'event_data_keys':schemas}
print(json.dumps(report,ensure_ascii=False))
