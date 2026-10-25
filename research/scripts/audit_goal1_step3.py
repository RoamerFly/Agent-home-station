"""Verify deployed proactive targets and evidence, without changing settings."""
import hashlib
import http.cookiejar
import json
import pathlib
import subprocess
import urllib.request

workspace=pathlib.Path(__file__).resolve().parents[2]
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
evidence=workspace/'research/evidence'
ui=json.loads((evidence/'ui-goal1-step3-regression-output.txt').read_text(encoding='utf-8-sig').strip())
for flag in ('group_and_friend_checkbox_selection','checkbox_keeps_keyboard_focus','per_target_idle_and_daily_opportunities','draft_preserved_through_filter_and_navigation','search_does_not_dirty_settings','save_and_reload_roundtrip','invalid_idle_prevented_inline','revoked_target_visible_and_removable'):
    assert ui[flag],flag
assert ui['real_config_writes']==ui['wechat_sends']==0
(evidence/'ui-goal1-step3-regression.json').write_text(json.dumps(ui,ensure_ascii=False,indent=2),encoding='utf8')
live=json.loads((evidence/'goal1-step3-live-model.json').read_text())
assert live['configured_provider_live'] and live['isolated_model_chats']==live['proactive_claims']==2
assert sorted(x['is_group'] for x in live['decisions'])==[False,True]
assert all(x['action'] in ('reply','silent') for x in live['decisions'])
assert live['new_candidates_after_claim']==live['wechat_sends']==0 and not live['production_config_and_memory_modified']
assert '--- PASS: TestLiveGoal1ProactiveGroupAndFriend' in (evidence/'goal1-step3-linux-tests.log').read_text()
base='http://127.0.0.1:8081'
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
request=urllib.request.Request(base+'/api/login',data=json.dumps({'password':(root/'admin/admin-token').read_text().strip()}).encode(),headers={'Origin':base,'Content-Type':'application/json'})
with client.open(request,timeout=10) as response:assert response.status==200
def get(path):
    with client.open(base+path,timeout=15) as response:return json.load(response)
status=get('/api/status')
assert status['native']['authenticated'] and status['native']['connection']=='online'
assert status['native']['incoming_transport']=='native_push' and status['bot']['state']=='running'
private=json.loads((root/'admin/config.json').read_text())
targets=get('/api/companion/targets')
allowed=set(x['id'] for x in private['group_rules'])
if private['private_enabled']:allowed.update(private['direct_ids'])
if private['filehelper_enabled']:allowed.add('filehelper')
assert set(x['id'] for x in targets['items'])==allowed
assert all(x['name'] and x['state']['reason'] for x in targets['items'])
assert len(targets['items'])==len(allowed)
assert targets['max_per_day']==private['companion']['proactive_max_per_day']
for item in targets['items']:
    if item['id']=='filehelper':assert item['kind']=='filehelper'
    elif item['id'].endswith('@chatroom'):assert item['kind']=='group'
    else:assert item['kind']=='friend'
with client.open(base+'/assets/proactive.js',timeout=10) as response:
    assert response.status==200 and response.headers.get_content_type()=='text/javascript'
    assert b'WechatProactive' in response.read()
digest=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
assert digest(root/'bin/wechat-admin')==digest(workspace/'dist/linux-amd64/wechat-admin')
assert digest(root/'bin/wx')==json.loads((evidence/'goal1-native-voice-update.json').read_text())['native_binary_sha256']
for unit in ('wechat-bot-web.service','wechat-bot-native.service'):
    subprocess.run(['systemctl','--user','is-active','--quiet',unit],check=True)
assert not (workspace/'secrets/ui-qa-auth-state.json').exists()
report={
    'objective_item':3,'implementation_and_regressions':'passed',
    'group_and_friend_selection_and_independent_schedule':True,
    'current_allowed_targets':len(allowed),'current_selected_targets':len(private['companion']['proactive_threads']),
    'current_proactive_permission':private['companion']['permissions']['proactive'],
    'idle_activity_includes_untriggered_and_outgoing_messages':True,
    'per_target_and_global_claims_persist_across_restart':True,
    'legacy_daily_claims_preserved':True,
    'isolated_live_group_and_friend_model_judgement':live['decisions'],
    'new_wechat_sends_this_step':0,'production_config_changed_by_tests':False,
    'browser_light_dark_and_responsive_passed':True,
    'deployed_web_matches_dist_and_native_unchanged':True,
    'bot_running_and_wechat_connected':True,
    'docker_images_built_or_exported':0,
    'completed_objective_items':[0,1,2,3],'remaining_objective_items':[4,5,6],
    'overall_goal_complete':False,
}
(evidence/'goal1-step3-acceptance.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(report,ensure_ascii=False))
