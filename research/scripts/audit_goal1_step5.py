"""Audit bounded tool execution, isolated live evidence and the installed web."""
import hashlib,http.cookiejar,json,pathlib,subprocess,urllib.request
workspace=pathlib.Path(__file__).resolve().parents[2]
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
evidence=workspace/'research/evidence'
history=json.loads((evidence/'goal1-step5-history.json').read_text())
assert history['legacy_newer_wave_reply_found']
assert any(x.get('tool')=='web_audio_search' and x.get('status')=='succeeded' for x in history['legacy_newer_wave_tool_events'])
assert any(x.get('tool')=='web_media_download' and x.get('status')=='failed' for x in history['legacy_newer_wave_tool_events'])
live=json.loads((evidence/'goal1-step5-live-resource.json').read_text())
assert live['real_catalog_and_download'] and live['model_selected_real_media'] and live['configured_provider_live']
assert live['staging_uploads']==2 and live['prepared_wav_bytes']>5_000_000
assert live['wechat_sends']==0 and live['isolated_native_only'] and not live['production_memory_modified']
downloads=[x for x in live['activities'] if x['tool']=='web_media_download']
assert len(downloads)==2 and all(x['status']=='succeeded' and x['bytes']==3316821 and x['media_count']==1 for x in downloads)
ui={}
for kind in ('tool','log-scroll'):
    value=json.loads((evidence/f'ui-goal1-step5-{kind}-output.txt').read_text(encoding='utf-8-sig').strip())
    (evidence/f'ui-goal1-step5-{kind}.json').write_text(json.dumps(value,ensure_ascii=False,indent=2),encoding='utf8')
    ui[kind]=value
assert ui['tool']['real_config_writes']==0 and ui['tool']['progress_and_final_message_visible_in_same_trace']
assert ui['tool']['explicit_false_preserved'] and ui['tool']['cached_catalog_distinguished_from_new_http']
assert ui['log-scroll']['polling_keeps_scroll_and_dom'] and ui['log-scroll']['filters_preserved']
tests=(evidence/'goal1-step5-targeted-tests.log').read_text(encoding='utf-8-sig')
for name in ('TestProgressHasOwnDurableReceiptAndDoesNotCompleteTask','TestAcknowledgedToolTaskAlwaysReportsItsTerminalOutcome','TestInterruptedToolJobIsTerminalInsteadOfReplayingSideEffects','TestUnknownProgressNeverResendsOrOverridesFinalTaskStatus','TestProgressFollowsModelChoicePrecedesToolAndNeverLeaksIntoPreview','TestGuessedDownloadURLIsNotExecutedAndDuplicateCallsAreNotReplayed','TestNegativeResourceRequestsAndCapabilityQuestionsDoNotForceDownloads','TestToolDiagnosticsPersistWithoutProviderData'):
    assert '--- PASS: '+name in tests
linux=(evidence/'goal1-step5-linux-transport.log').read_text()
for name in ('TestStdioCancellationClosesInheritedOutputPipe','TestPublicGETRecoveryIsBoundedAndReportsSafeStages','TestPublicGETRetryWaitCancellationAndCatalogWaitDoNotHang'):
    assert '--- PASS: '+name in linux
digest=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
paths=[root/'admin/config.json',root/'storage-key',root/'api-token']
before=[digest(p) for p in paths]
base='http://127.0.0.1:8081'
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
req=urllib.request.Request(base+'/api/login',data=json.dumps({'password':(root/'admin/admin-token').read_text().strip()}).encode(),headers={'Origin':base,'Content-Type':'application/json'})
with client.open(req,timeout=10) as response:assert response.status==200
def get(path):
    with client.open(base+path,timeout=15) as response:return json.load(response)
status=get('/api/status');config=get('/api/config')
private=json.loads((root/'admin/config.json').read_text())
assert config['behavior']['acknowledge_tools']==private.get('behavior',{}).get('acknowledge_tools',True)
assert before==[digest(p) for p in paths]
assert status['native']['authenticated'] and status['native']['connection']=='online'
assert status['native']['incoming_transport']=='native_push' and status['bot']['state']=='running'
assert digest(root/'bin/wechat-admin')==digest(workspace/'dist/linux-amd64/wechat-admin')
assert digest(root/'bin/wx')==json.loads((evidence/'goal1-native-voice-update.json').read_text())['native_binary_sha256']
for unit in ('wechat-bot-web.service','wechat-bot-native.service'):
    subprocess.run(['systemctl','--user','is-active','--quiet',unit],check=True)
queue=json.loads((root/'bot-state/messages-v2.json').read_text())
active=[x for x in queue['inbox'] if x['phase'] in ('processing','tool_running','sending')]
assert not active,'An incoming task is currently active; inspect its real handle before accepting this snapshot'
assert not (workspace/'secrets/ui-qa-auth-state.json').exists()
report={
    'objective_item':5,'implementation_and_regressions':'passed',
    'legacy_newer_wave_did_call_search_and_download':True,
    'legacy_22_second_transport_subtype':'not_recorded_cannot_reconstruct',
    'real_happy_alley_downloads_and_audio_preparation':len(downloads),
    'real_download_ms':[x['duration_ms'] for x in downloads],
    'configured_model_chose_tools_and_real_attachment':True,
    'live_network_retry_exercised':any(x['get_retries']>0 for x in live['activities']),
    'read_only_retry_recovery_verified_with_synthetic_failures':True,
    'stdio_inherited_pipe_cancellation_verified_on_windows_and_linux':True,
    'catalog_wait_cancellable_and_cache_explicit':True,
    'resource_urls_grounded_and_repeated_web_calls_not_reexecuted':True,
    'progress_and_final_have_distinct_durable_receipts':True,
    'progress_replay_and_false_task_completion_prevented':True,
    'acknowledgement_enabled_in_current_config':config['behavior']['acknowledge_tools'],
    'new_wechat_sends_this_step':0,'live_progress_phone_receipt_verified':False,
    'native_staging_in_live_resource_test':'isolated_fake_upload_receiver',
    'interrupted_effectful_tool_jobs_not_automatically_replayed':True,
    'independent_conversation_and_interruptible_speech_scheduler':'remaining_objective_item_6',
    'browser_tool_diagnostics_and_scroll_regressions_passed':True,
    'deployed_web_matches_dist_and_native_unchanged':True,
    'bot_running_and_wechat_connected':True,
    'docker_images_built_or_exported':0,
    'completed_objective_items':[0,1,2,3,4,5],'remaining_objective_items':[6],
    'overall_goal_complete':False,
}
(evidence/'goal1-step5-acceptance.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(report,ensure_ascii=False))
