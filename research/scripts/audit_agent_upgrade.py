"""Read-only final audit of the current local Agent upgrade."""
import hashlib
import http.cookiejar
import json
import urllib.request
from pathlib import Path

workspace = Path(__file__).resolve().parents[2]
root = Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
evidence = workspace/'research/evidence'
ui_log = (evidence/'agent-upgrade-ui-output.log').read_text(encoding='utf-8-sig')
ui = next(json.loads(line) for line in ui_log.splitlines() if line.startswith('{"pages":'))
(evidence/'agent-upgrade-ui.json').write_text(json.dumps(ui,ensure_ascii=False,indent=2),encoding='utf8')
events = [json.loads(line) for line in (evidence/'agent-upgrade-go-tests.jsonl').read_text(encoding='utf-8-sig').splitlines() if line.startswith('{')]
assert not any(e.get('Action')=='fail' for e in events)
assert not ui['mutations'] and not ui['runtime_errors'] and not ui['dialogs'] and not ui['duplicate_ids']
assert not any(x['overflow'] for x in ui['layouts'])
assert ui['scroll_before']==ui['scroll_after']
jar = http.cookiejar.CookieJar()
client = urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(jar))
base='http://127.0.0.1:8081'
def get(route):
    with client.open(base+route,timeout=20) as r:
        return json.load(r)
password=(root/'admin/admin-token').read_text().strip()
req=urllib.request.Request(base+'/api/login',data=json.dumps({'password':password}).encode(),headers={'Content-Type':'application/json','Origin':base})
with client.open(req,timeout=10) as r:
    assert r.status==200
status=get('/api/status')
agent=get('/api/agent')
assert status['version']=='0.7.0-agent-local'
assert status['native']['authenticated'] and status['bot']['state']=='running'
sha=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
assert sha(workspace/'dist/linux-amd64/wechat-admin')==sha(root/'bin/wechat-admin')
for name in ['app.js','agent.js','app.css','logs.js','index.html']:
    route='/' if name=='index.html' else '/assets/'+name
    with client.open(base+route,timeout=10) as r:
        assert r.read()==(workspace/'internal/admin/static'/name).read_bytes(), name
live=json.loads((evidence/'agent-upgrade-live-host.json').read_text())
assert live['file_created'] and live['completed_checkpoint'] and not live['model_error'] and live['wechat_sends']==0
assert not live['production_permissions_changed']
race=(evidence/'agent-upgrade-race.log').read_text(encoding='utf-8-sig')
assert 'FAIL' not in race and race.count('ok  ')>=5
report={
 'version':status['version'],'web_url':base+'/#agent','bot_state':status['bot']['state'],
 'wechat_authenticated':status['native']['authenticated'],'web_sha256':sha(root/'bin/wechat-admin'),
 'go_test_packages':sum(e.get('Action')=='pass' and 'Test' not in e for e in events),
 'go_named_tests':sum(e.get('Action')=='pass' and 'Test' in e for e in events),
 'race_packages':5,'ui_pages':len(ui['pages']),'ui_layouts':len(ui['layouts']),
 'runtime_static_assets_match':True,'log_scroll_preserved':True,
 'host_mode':agent['settings']['host']['mode'],'action_steps':agent['settings']['max_steps'],
 'real_model_isolated_host_test':live,'docker_image_built_or_exported':False,
 'limitations':['Full shell uses OS service-user privileges, not a path sandbox',
                'No live WeChat management action or privileged owner conversation sent during testing',
                'Interrupted external effects require review; completed effects cannot be undone by cancellation',
                'Background tasks require proactive permission, selected target, quiet-hour and budget admission'],
 'passed':True,
}
(evidence/'agent-upgrade-acceptance.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps({k:report[k] for k in ['version','passed','go_test_packages','go_named_tests','ui_pages','host_mode','action_steps']},ensure_ascii=False))
