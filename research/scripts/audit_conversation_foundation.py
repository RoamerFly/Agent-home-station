"""Audit this stage only. This does not certify all of goal 1 item 6 complete."""
import hashlib,http.cookiejar,json,pathlib,subprocess,urllib.request
workspace=pathlib.Path(__file__).resolve().parents[2];root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024');evidence=workspace/'research/evidence';base='http://127.0.0.1:8081'
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
with client.open(urllib.request.Request(base+'/api/login',data=json.dumps({'password':(root/'admin/admin-token').read_text().strip()}).encode(),headers={'Origin':base,'Content-Type':'application/json'}),timeout=15)as r:assert r.status==200
def get(path):
    with client.open(base+path,timeout=15)as r:return json.load(r)
status=get('/api/status');config=get('/api/config');raw=json.loads((root/'admin/config.json').read_text());settings=config['companion']['conversation']
assert status['native']['authenticated']and status['native']['connection']=='online'and status['bot']['state']=='running'
expected=raw['companion'].get('conversation')or{'enabled':True,'aggregate_ms':2000,'recheck_on_message':True,'allow_wake':False,'wake_min_seconds':30,'wake_max_seconds':600}
assert settings==expected
for key in ['quiet_start','quiet_end','timezone','proactive_threads','proactive_max_per_day']:assert config['companion'][key]==raw['companion'][key]
digest=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
assert digest(root/'bin/wechat-admin')==digest(workspace/'dist/linux-amd64/wechat-admin')
native=json.loads((evidence/'native-share-quote-update.json').read_text());assert digest(root/'bin/wx')==native['native_binary_sha256']
for unit in ['wechat-bot-web.service','wechat-bot-native.service']:subprocess.run(['systemctl','--user','is-active','--quiet',unit],check=True)
read=lambda n:json.loads((evidence/n).read_text(encoding='utf-8-sig'))
live=read('goal1-step6-live-plan.json');assert len(live['decision']['messages'])==2 and live['review']['action']=='cancel'and not live['review_error']and live['wechat_sends']==0
ui=read('ui-conversation-plan.json');assert ui['explicit_false_preserved']and ui['invalid_range_blocked']and ui['reload_uses_real_saved_settings']and ui['poll_keeps_scroll']['before']==ui['poll_keeps_scroll']['after']and all(not x['overflow']and not x['fields_overflow']for x in ui['layouts'])
tests=(evidence/'goal1-step6-foundation-tests.log').read_text(encoding='utf-8-sig');assert 'FAIL'not in tests
linux=(evidence/'goal1-step6-linux-queue.log').read_text(encoding='utf-8-sig')
for name in ['TestSemanticPlanWaitsForFreshInboxThenCanReplaceOnlyUnsentTail','TestPlanRestartKeepsExactTraceQuoteAndDeduplicatesAcceptedMemory','TestSleepingPlanDoesNotBlockAnotherConversationAndCancelDoesNotCancelMedia','TestShortMessageAggregationPreservesEachIdentityAndDoesNotMixSpeakers','TestWakeIsIndependentBoundedAndOnlyForOwnerSelectedTargets','TestPlanPollsExistingReceiptWithoutWaitingForResendTimerOrSubmittingAgain','TestNewContextPreventsSubmissionOfPreparedOldText']:assert '--- PASS: '+name in linux
assert not(workspace/'secrets/ui-qa-auth-state.json').exists()
queue=json.loads((root/'bot-state/messages-v2.json').read_text());assert queue['version']==1
report={'web_url':base+'/#companion','runtime_matches_build':True,'web_sha256':digest(root/'bin/wechat-admin'),'native_session_unchanged':True,'bot_running':True,'semantic_text_plan_queue_implemented':True,'fresh_inbox_check_before_first_text':True,'unsent_tail_pause_continue_replace_delay_cancel_supported':True,'individual_delivery_keys_and_accepted_memory_dedup':True,'short_message_aggregation_preserves_speaker_and_message_identity':True,'waiting_text_plan_does_not_block_other_queued_thread':True,'wake_scheduler_independent_of_new_question_with_owner_target_budget_quiet_checks':True,'wake_opt_in_preserved':settings['allow_wake'],'real_model_semantic_plan_and_cancel_test':'passed','isolated_wechat_sends':0,'browser_false_values_bounds_theme_mobile_scroll_checks':'passed','owner_saved_settings_not_overwritten_by_ui_test':True,'docker_images_built_or_exported':0,'item6_complete':False,'overall_goal_complete':False,'remaining_work':['Decouple long model/tool calls from inbox consumption and conversation scheduling','Complete end-to-end regression for interruption during long generation/tools and mixed media/text scheduling','Audit original numbered requirements 0-6 against final runtime evidence']}
(evidence/'goal1-step6-foundation-acceptance.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8');print(json.dumps(report,ensure_ascii=False))
