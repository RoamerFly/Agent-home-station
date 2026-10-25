"""Audit the deployed step-1 result without configuration changes or message sends."""
import datetime
import hashlib
import http.cookiejar
import json
import pathlib
import subprocess
import urllib.request

workspace=pathlib.Path(__file__).resolve().parents[2]
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
evidence=workspace/'research/evidence'
history=json.loads((evidence/'goal1-step1-history.json').read_text())
live=json.loads((evidence/'goal1-step1-live-model.json').read_text())
ui=json.loads((evidence/'ui-goal1-step1-regression.json').read_text())
assert live['configured_provider_live'] and live['isolated_model_chats']==2 and live['question_reviewed_during_cooldown']
assert live['question_decision'] in ('reply','silent') and live['question_reason']!='decision_cooldown' and live['wechat_sends']==0
assert ui['failure_details_include_status_and_attempts'] and ui['polling_keeps_scroll_and_dom'] and ui['real_mutations']==0
linux=(evidence/'goal1-step1-linux-tests.log').read_text()
for test in ('TestPublicVideoRemuxDropsExtraTracksAndPreservesDuration','TestLiveGoal1ModelQuestionJudgement'):
    assert '--- PASS: '+test in linux
assert 'video=1 audio=1 duration=2.000s (source 2.000s)' in linux
failure=next(x for x in history['activity'] if x['event']=='model_failed' and x.get('reason')=='model request unavailable')
processing=next(x for x in history['activity'] if x['event']=='message_processing' and x['message_id']==failure['message_id'])
stamp=lambda x:datetime.datetime.fromisoformat(x.replace('Z','+00:00'))
legacy_elapsed_ms=round((stamp(failure['time'])-stamp(processing['time'])).total_seconds()*1000)
assert any(x.get('reason')=='decision_cooldown' for x in history['activity'])
videos=[x for x in history['video_jobs'] if x['filename']=='generated-video.f4v']
assert any(x['state']=='failed' and x['phase']=='preparing' for x in videos)
assert any(x['state']=='accepted' and x['result'].get('nativeReceipt',{}).get('status')=='server_accepted' and x['result'].get('uploadReadbackVerified') for x in videos)
base='http://127.0.0.1:8081'
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
req=urllib.request.Request(base+'/api/login',data=json.dumps({'password':(root/'admin/admin-token').read_text().strip()}).encode(),headers={'Origin':base,'Content-Type':'application/json'})
with client.open(req,timeout=10) as response: assert response.status==200
def get(path):
    with client.open(base+path,timeout=15) as response:return json.load(response)
status=get('/api/status')
print(json.dumps({'current_native':status['native'],'current_bot':status['bot']},ensure_ascii=False),flush=True)
# Native account_status exposes "online"; "connected" is its internal desired state.
assert status['native']['authenticated'] and status['native']['connection']=='online' and status['native']['incoming_transport']=='native_push' and status['bot']['state']=='running'
video=get('/api/tools/lookup?key=repair-043-filehelper-video-20261024-remux-check')
assert video['state']=='accepted' and video.get('server_id')
logs=get('/api/logs')['items']
assert all(not x.get('model_attempts') for x in logs if x.get('reason')=='model request unavailable')
digest=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
assert digest(root/'bin/wechat-admin')==digest(workspace/'dist/linux-amd64/wechat-admin')
assert digest(root/'bin/wx')==digest(workspace/'dist/linux-amd64/wx-linux-native')
assert digest(root/'bin/wx')==json.loads((evidence/'goal1-native-voice-update.json').read_text())['native_binary_sha256']
for unit in ('wechat-bot-web.service','wechat-bot-native.service'):
    subprocess.run(['systemctl','--user','is-active','--quiet',unit],check=True)
report={'objective_item':1,'implementation_and_regressions':'passed','legacy_model_failure_elapsed_ms':legacy_elapsed_ms,'legacy_transport_subtype':'not_recorded_cannot_reconstruct','video_failure_previously_fixed_and_current_receipt_confirmed':True,'multitrack_video_duration_preserved':True,'question_reaches_model_during_cooldown':True,'judgement_may_reply_or_choose_silence':True,'provider_retry_max_attempts':2,'permanent_errors_do_not_retry':True,'model_retry_recovery_exercised_with_synthetic_upstream':True,'live_provider_outage_recovery_exercised':bool(live.get('request_events')),'new_wechat_sends_this_step':0,'historical_messages_replayed':False,'deployed_web_matches_dist_and_native_unchanged':True,'bot_running_and_wechat_connected':True,'docker_images_built_or_exported':0,'completed_objective_items':[0,1],'remaining_objective_items':[2,3,4,5,6],'overall_goal_complete':False}
(evidence/'goal1-step1-acceptance.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(report,ensure_ascii=False))
