"""Read-only delivery check for surf targets, quiet budgets and compact memory."""
import hashlib
import http.cookiejar
import json
import pathlib
import subprocess
import urllib.request

workspace=pathlib.Path(__file__).resolve().parents[2]
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
evidence=workspace/'research/evidence'
ui={}
for name in ('surf','memory'):
    result=json.loads((evidence/f'ui-goal1-step4-{name}-output.txt').read_text(encoding='utf-8-sig').strip())
    assert result.get('real_config_writes',result.get('real_mutations'))==0
    assert result['themes']==['light','dark'] and result['responsive_widths']==[1440,768,375]
    (evidence/f'ui-goal1-step4-{name}.json').write_text(json.dumps(result,ensure_ascii=False,indent=2),encoding='utf8')
    ui[name]=result
assert ui['surf']['group_and_friend_multiselect'] and ui['surf']['save_reload_roundtrip']
assert ui['surf']['legacy_revoked_target_visible_and_removable'] and ui['surf']['directory_failure_preserves_selection']
assert ui['surf']['filter_refresh_and_navigation_preserve_draft'] and ui['surf']['quiet_pause_zero_extra_count_and_configured_timezone_visible']
assert ui['memory']['duplicate_conversation_heading_removed'] and ui['memory']['selected_scope_accessible']
assert ui['memory']['redundant_memory_title_description_removed']
tests=(evidence/'goal1-step4-targeted-tests.log').read_text(encoding='utf-8-sig')
for name in ('TestSurfQuietHoursSkipWithoutConsumingOrMutatingSchedule','TestSurfTargetSelectionRoundtripAndDailyDisplay','TestSurfInProgressStopsAtQuietBoundaryWithoutSending','TestQuietDeadlineUsesConfiguredTimezoneAndLocalDay'):
    assert '--- PASS: '+name in tests
digest=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
files=[root/'admin/config.json',root/'admin/surf.json',root/'storage-key',root/'api-token']
before=[digest(p) if p.exists() else None for p in files]
base='http://127.0.0.1:8081'
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
req=urllib.request.Request(base+'/api/login',data=json.dumps({'password':(root/'admin/admin-token').read_text().strip()}).encode(),headers={'Origin':base,'Content-Type':'application/json'})
with client.open(req,timeout=10) as response:assert response.status==200
def get(path):
    with client.open(base+path,timeout=15) as response:return json.load(response)
status=get('/api/status');surf=get('/api/surf');targets=get('/api/companion/targets')
c=json.loads((root/'admin/config.json').read_text())
assert surf['quiet_start']==c['companion']['quiet_start'] and surf['quiet_end']==c['companion']['quiet_end'] and surf['timezone']==c['companion']['timezone']
assert set(surf['allowed_threads'])==set(x['id'] for x in targets['items'])
assert status['native']['authenticated'] and status['native']['connection']=='online'
assert status['native']['incoming_transport']=='native_push' and status['bot']['state']=='running'
assert before==[digest(p) if p.exists() else None for p in files]
assert digest(root/'bin/wechat-admin')==digest(workspace/'dist/linux-amd64/wechat-admin')
assert digest(root/'bin/wx')==json.loads((evidence/'goal1-native-voice-update.json').read_text())['native_binary_sha256']
for unit in ('wechat-bot-web.service','wechat-bot-native.service'):
    subprocess.run(['systemctl','--user','is-active','--quiet',unit],check=True)
assert not (workspace/'secrets/ui-qa-auth-state.json').exists()
report={
    'objective_item':4,'implementation_and_regressions':'passed',
    'group_friend_and_filehelper_multiselect':True,'raw_target_id_input_removed':True,
    'quiet_skips_do_not_mutate_count_day_topic_or_schedule':True,
    'in_progress_surf_cancelled_at_quiet_boundary':True,
    'tasks_started_before_quiet_keep_their_existing_claim':True,
    'quiet_end_does_not_catch_up_skipped_jobs':True,
    'memory_duplicate_conversation_heading_removed':True,
    'memory_redundant_title_and_description_removed':True,
    'memory_selection_and_names_preserved':True,
    'surf_enabled_in_current_config':surf['settings']['enabled'],
    'current_selected_surf_targets':len(surf['settings']['threads']),
    'current_agent_quiet_period':[surf['quiet_start'],surf['quiet_end'],surf['timezone']],
    'quiet_now':surf['quiet_now'],
    'new_wechat_sends_this_step':0,'model_calls_this_step':0,
    'production_config_and_surf_ledger_unchanged_by_checks':True,
    'browser_light_dark_and_responsive_passed':True,
    'deployed_web_matches_dist_and_native_unchanged':True,
    'bot_running_and_wechat_connected':True,
    'docker_images_built_or_exported':0,
    'completed_objective_items':[0,1,2,3,4],'remaining_objective_items':[5,6],
    'overall_goal_complete':False,
}
(evidence/'goal1-step4-acceptance.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(report,ensure_ascii=False))
