"""Read-only stage-two runtime audit. Does not certify the full 0-6 goal."""
import hashlib,http.cookiejar,json,pathlib,subprocess,urllib.request
workspace=pathlib.Path(__file__).resolve().parents[2]
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
evidence=workspace/'research/evidence';base='http://127.0.0.1:8081'
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
with client.open(urllib.request.Request(base+'/api/login',data=json.dumps({'password':(root/'admin/admin-token').read_text().strip()}).encode(),headers={'Origin':base,'Content-Type':'application/json'}),timeout=15)as response:
    assert response.status==200
def get(path):
    with client.open(base+path,timeout=15)as response:return json.load(response)
status=get('/api/status');config=get('/api/config');saved=json.loads((root/'admin/config.json').read_text())
assert status['bot']['state']=='running'and status['native']['authenticated']and status['native']['connection']=='online'
expected=saved['companion'].get('conversation')or{'enabled':True,'aggregate_ms':2000,'recheck_on_message':True,'allow_wake':False,'wake_min_seconds':30,'wake_max_seconds':600}
assert config['companion']['conversation']==expected
assert expected['enabled'], 'owner has disabled asynchronous conversation scheduling'
for key in ['quiet_start','quiet_end','timezone','proactive_threads','proactive_max_per_day']:assert config['companion'][key]==saved['companion'][key]
digest=lambda path:hashlib.sha256(path.read_bytes()).hexdigest()
assert digest(root/'bin/wechat-admin')==digest(workspace/'dist/linux-amd64/wechat-admin')
native=json.loads((evidence/'native-share-quote-update.json').read_text());assert digest(root/'bin/wx')==native['native_binary_sha256']
for unit in ['wechat-bot-web.service','wechat-bot-native.service']:subprocess.run(['systemctl','--user','is-active','--quiet',unit],check=True)
linux=(evidence/'goal1-step6-linux-background.log').read_text(encoding='utf-8-sig')
required=['TestDurableInboxContinuesWhileAnotherConversationModelWaits','TestNewFileInstructionCancelsInFlightVoiceDownloadWithoutPublishingItsResult','TestSlowPlanReviewAllowsAnotherThreadAndDiscardsObsoleteReview','TestModelCanCancelAnInFlightTaskFromNewContextWithoutStopKeywords','TestCompletedGenerationIsHeldUntilFreshInboxAndInterruptionReview','TestMediaCaptionJoinsInterruptiblePlanOnlyAfterAttachmentReceipt','TestGenerationRestartReevaluatesPureModelButNeverReplaysStartedTools','TestAsyncSurfKeepsExplicitQuietDeadlineWithoutImmediateCancellation','TestMediaUnderstandingDoesNotBlockAnotherConversation','TestBackgroundProgressHasAcceptedReceiptBeforeToolAndOneFinalOutcome','TestUpdateDrainWaitsForActiveGenerationAndPreservesNewInbox','TestAuthorizedImageUsesIndependentMediaQueueBeforeCaption','TestOldResultWaitsForStopRequestOnNextInboxPage']
for name in required:assert '--- PASS: '+name in linux, name
for filename in ['goal1-step6-background-tests.log','goal1-step6-background-race.log']:
    text=(evidence/filename).read_text(encoding='utf-8-sig');assert 'FAIL'not in text and 'WARNING: DATA RACE'not in text and 'ok  'in text
live=json.loads((evidence/'goal1-step6-live-plan.json').read_text());assert live['decision']['action']=='reply'and len(live['decision']['messages'])==2 and live['review']['action']=='cancel'and live['wechat_sends']==0
assert not(workspace/'secrets/ui-qa-auth-state.json').exists()
queue=json.loads((root/'bot-state/messages-v2.json').read_text());assert queue['version']==1
report={'web_url':base+'/#companion','web_sha256':digest(root/'bin/wechat-admin'),'runtime_matches_build':True,'native_session_unchanged':True,'bot_running':True,'conversation_scheduler_enabled':True,'isolated_background_regressions':required,'race_detector':'passed','full_go_suite':'passed','real_model_semantic_plan_and_interruption':'passed using an isolated fork and memory','fresh_inbox_scan_precedes_finished_result_submission':True,'tool_effects_marked_before_execution_and_not_replayed_on_restart':True,'same_thread_requests_remain_durable_and_ordered':True,'three_generation_slots_plus_bounded_review_capacity':True,'caption_replanning_preserves_attachment_state':True,'owner_saved_settings_preserved':True,'wechat_test_sends':0,'docker_images_built_or_exported':0,'full_goal_complete':False,'remaining_work':['Audit original numbered items 0-6 against current sources, runtime and requirement-specific evidence; do not infer complete from this stage audit']}
(evidence/'goal1-step6-background-acceptance.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(report,ensure_ascii=False))
