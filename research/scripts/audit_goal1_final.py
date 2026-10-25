"""Requirement-by-requirement final audit; reads existing receipts, never sends.

Do not run earlier milestone auditors to overwrite their historical claims.
This audit binds fresh test evidence and installed artifacts to the original
numbered requirements and records untested phone playback separately.
"""
import datetime,hashlib,http.cookiejar,json,pathlib,re,sqlite3,subprocess,urllib.parse,urllib.request

workspace=pathlib.Path(__file__).resolve().parents[2]
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
evidence=workspace/'research/evidence';base='http://127.0.0.1:8081'
read=lambda name:json.loads((evidence/name).read_text(encoding='utf-8-sig'))
digest=lambda path:hashlib.sha256(path.read_bytes()).hexdigest()
private_files=[root/'admin/config.json',root/'storage-key',root/'api-token']
before=[digest(p)for p in private_files]
saved=json.loads(private_files[0].read_text())
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
with client.open(urllib.request.Request(base+'/api/login',data=json.dumps({'password':(root/'admin/admin-token').read_text().strip()}).encode(),headers={'Origin':base,'Content-Type':'application/json'}),timeout=15)as response:assert response.status==200
def get(path):
    with client.open(base+path,timeout=20)as response:return json.load(response)
def text(name):return (evidence/name).read_text(encoding='utf-8-sig')
events=[json.loads(line)for line in text('goal1-final-go-tests.jsonl').splitlines()if line.strip()]
assert not any(x.get('Action')=='fail'for x in events)
passed={x['Test']for x in events if x.get('Action')=='pass'and x.get('Test')}
packages={x['Package']for x in events if x.get('Action')=='pass'and not x.get('Test')}
assert len(packages)>20
coverage={
 1:['TestModelTransientFailureRecoversSameInputAndReportsAttempts','TestLongRetryAfterIsRespectedAndCancellationStopsBackoff','TestNetworkErrorsAreBoundedAndNeverExposePrivateDetails','TestResponseReadFailureIsNotMisreportedAsSizeLimit','TestHistoricalToolQuestionPassesBothCooldownsForJudgement','TestSecondPersonQuestionsRequestJudgementWithoutInventingMentions','TestQuestionReviewRetainsBudgetsPermissionsAndQuietHours'],
 2:['TestSpeakerNamePriorityExactIdentityAndRoomScopedCache','TestMemoryNamesPersistWithoutChangingIdentityContentOrClaims','TestNamePriorityAndDisabledMemoryDoNotPersistMetadata','TestMemoryAPIBackfillsOldNamesAndDoesNotExposeAccountIdentity'],
 3:['TestProactiveTargetsIsolationTimingBudgetsAndRestart','TestProactiveLegacyLedgerAndActivityRemainPrivate','TestProactiveConfigurationRoundtripAndAllowedNamedTargets','TestProactiveWorkerSendsToSelectedGroupAndFriendOrChoosesSilence','TestUntriggeredGroupActivityPostponesProactiveWithoutMemorizingBody'],
 4:['TestSurfQuietHoursSkipWithoutConsumingOrMutatingSchedule','TestSurfTargetSelectionRoundtripAndDailyDisplay','TestSurfRandomScheduleIsBoundedVariedAndOldConfigLoads','TestSurfInProgressStopsAtQuietBoundaryWithoutSending','TestQuietDeadlineUsesConfiguredTimezoneAndLocalDay'],
 5:['TestPublicGETRecoveryIsBoundedAndReportsSafeStages','TestPublicGETRetryWaitCancellationAndCatalogWaitDoNotHang','TestStdioCancellationClosesInheritedOutputPipe','TestGuessedDownloadURLIsNotExecutedAndDuplicateCallsAreNotReplayed','TestNegativeResourceRequestsAndCapabilityQuestionsDoNotForceDownloads','TestProgressFollowsModelChoicePrecedesToolAndNeverLeaksIntoPreview','TestProgressHasOwnDurableReceiptAndDoesNotCompleteTask','TestAcknowledgedToolTaskAlwaysReportsItsTerminalOutcome','TestInterruptedToolJobIsTerminalInsteadOfReplayingSideEffects','TestUnknownProgressNeverResendsOrOverridesFinalTaskStatus','TestNewSongCannotUseOldLocalTrackAsSearchEvidence'],
 6:['TestSemanticPlanWaitsForFreshInboxThenCanReplaceOnlyUnsentTail','TestPlanRestartKeepsExactTraceQuoteAndDeduplicatesAcceptedMemory','TestShortMessageAggregationPreservesEachIdentityAndDoesNotMixSpeakers','TestWakeIsIndependentBoundedAndOnlyForOwnerSelectedTargets','TestAsyncWakeRunsWithoutNewQuestionAndDoesNotReplayAfterRestart','TestPendingPlanDelayAndContinueRetainUnsentContentWithoutBlockingOtherThread','TestDurableInboxContinuesWhileAnotherConversationModelWaits','TestNewFileInstructionCancelsInFlightVoiceDownloadWithoutPublishingItsResult','TestSlowPlanReviewAllowsAnotherThreadAndDiscardsObsoleteReview','TestModelCanCancelAnInFlightTaskFromNewContextWithoutStopKeywords','TestCompletedGenerationIsHeldUntilFreshInboxAndInterruptionReview','TestMediaCaptionJoinsInterruptiblePlanOnlyAfterAttachmentReceipt','TestGenerationRestartReevaluatesPureModelButNeverReplaysStartedTools','TestMediaUnderstandingDoesNotBlockAnotherConversation','TestUpdateDrainWaitsForActiveGenerationAndPreservesNewInbox','TestOldResultWaitsForStopRequestOnNextInboxPage','TestConcurrentToolForksKeepTraceAndMediaHandlesIsolated','TestMemoryIsolationPersistenceAndClearPreservesClaims','TestSocialReplyOrSilenceIsChosenByModelWithoutMandatoryLabels']}
for item,names in coverage.items():
    for name in names:assert name in passed,f'item {item}: missing executed passing test {name}'
assert 'WARNING: DATA RACE'not in text('goal1-step6-background-race.log')and 'FAIL'not in text('goal1-step6-background-race.log')
for filename,names in {
 'goal1-final-linux-conversation.log':['TestAsyncWakeRunsWithoutNewQuestionAndDoesNotReplayAfterRestart','TestPendingPlanDelayAndContinueRetainUnsentContentWithoutBlockingOtherThread'],
 'goal1-step5-linux-transport.log':['TestStdioCancellationClosesInheritedOutputPipe','TestPublicGETRecoveryIsBoundedAndReportsSafeStages','TestPublicGETRetryWaitCancellationAndCatalogWaitDoNotHang'],
 'goal1-step1-linux-tests.log':['TestLiveGoal1ModelQuestionJudgement','TestPublicVideoRemuxDropsExtraTracksAndPreservesDuration'],
 'goal1-step5-linux-tests.log':['TestLiveGoal1ResourceSearchDownloadAndModelChoice'],
}.items():
    for name in names:assert '--- PASS: '+name in text(filename),(filename,name)
native_tests=text('goal1-final-native-tests.log')
assert 'test media::tests::voice_pacing_tracks_recording_time_and_cancels_before_upload ... ok'in native_tests
assert 'test client::voice_handshake_tests::only_uncommitted_prefix_retries_and_final_submission_never_repeats ... ok'in native_tests
assert 'test result: ok. 187 passed; 0 failed'in native_tests and 'test result: ok. 138 passed; 0 failed'in native_tests
status=get('/api/status');config=get('/api/config');surf=get('/api/surf');targets=get('/api/companion/targets')
assert status['native']['authenticated']and status['native']['connection']=='online'and status['native']['incoming_transport']=='native_push'and status['bot']['state']=='running'
web_hash=digest(root/'bin/wechat-admin');native_hash=digest(root/'bin/wx')
assert web_hash==digest(workspace/'dist/linux-amd64/wechat-admin')
assert native_hash==digest(workspace/'dist/linux-amd64/wx-linux-native')==read('native-share-quote-update.json')['native_binary_sha256']
for unit in ['wechat-bot-web.service','wechat-bot-native.service']:subprocess.run(['systemctl','--user','is-active','--quiet',unit],check=True)
builder=subprocess.check_output(['docker','inspect','--format','{{.State.Status}}','wechat-voice-goal1-builder'],text=True).strip();assert builder=='exited'
for filename in ['app.js','proactive.js','surf.js','logs.js','app.css']:
    with client.open(base+'/assets/'+filename,timeout=15)as response:assert response.read()==(workspace/'internal/admin/static'/filename).read_bytes(),filename+' installed asset differs'
for key in ['quiet_start','quiet_end','timezone','proactive_threads','proactive_rules','proactive_max_per_day']:
    assert config['companion'].get(key)==saved['companion'].get(key)
settings=config['companion']['conversation'];assert settings['enabled']and settings['recheck_on_message']
expected=saved['companion'].get('conversation')or{'enabled':True,'aggregate_ms':2000,'recheck_on_message':True,'allow_wake':False,'wake_min_seconds':30,'wake_max_seconds':600}
assert settings==expected
assert config['behavior']['acknowledge_tools']==saved['behavior']['acknowledge_tools']
allowed={x['id']for x in saved['group_rules']}
if saved['private_enabled']:allowed.update(saved['direct_ids'])
if saved['filehelper_enabled']:allowed.add('filehelper')
assert {x['id']for x in targets['items']}==allowed==set(surf['allowed_threads'])
assert all(x['name']and x['state']['reason']for x in targets['items'])
surf_saved=json.loads((root/'admin/surf.json').read_text())['settings']
for key in ['enabled','threads','topics','interval_minutes','interval_max_minutes','max_sessions_per_day','discovery']:assert surf['settings'][key]==surf_saved[key]
db=sqlite3.connect('file:'+str(root/'accounts/background-test-20261024/agent/control.sqlite')+'?mode=ro',uri=True)
receipts={}
for label,key in [('paced_song','goal1-nonfinal-prefix-recovery-happy-alley-20261025'),('fixed_video','repair-043-filehelper-video-20261024-remux-check')]:
    result=get('/api/tools/lookup?key='+urllib.parse.quote(key))
    assert result['state']=='accepted'
    row=db.execute('select state,data_json from media_jobs where id=?',(result['id'],)).fetchone();assert row and row[0]=='accepted'
    data=json.loads(row[1]);receipts[label]=data.get('result',{})
song=read('goal1-voice-prefix-recovery.json')
assert song['all_notes_have_accepted_receipts']and song['inter_note_gap_ms']>=40924 and len(song['notes'])==2
chunks=receipts['paced_song']['chunks'];assert len(chunks)==2 and all(x['state']=='accepted'and x.get('serverMessageId')for x in chunks)
assert receipts['fixed_video']['nativeReceipt']['status']=='server_accepted'and receipts['fixed_video']['uploadReadbackVerified']
memory_state=get('/api/companion/state');named_users,named_assistants=0,0
for entry in memory_state['scopes']:
    scope=get('/api/companion/memory/'+entry['id'])
    for turn in scope['messages']:
        if turn['role']=='user'and turn.get('sender')and turn.get('sender_name'):named_users+=1
        if turn['role']=='assistant'and turn.get('sender_name_source')=='character':named_assistants+=1
assert named_users>0 and named_assistants>0
ui=read('goal1-final-ui.json');assert ui['mutations']==0 and not ui['runtime_errors']and len(ui['pages'])==8 and all(x['visible']for x in ui['pages'])and all(not x['overflow']for x in ui['layouts'])
assert ui['proactive_target_checkboxes']==len(allowed)==ui['surf_target_checkboxes']and ui['raw_surf_target_input_removed']and ui['memory_redundant_description_absent']and ui['log_filters_present']
assert ui['logs_scroll']['before']==ui['logs_scroll']['after']and ui['logs_scroll']['before']['list']>0
assert read('ui-goal1-step3-regression.json')['save_and_reload_roundtrip']
assert read('ui-goal1-step4-surf.json')['save_reload_roundtrip']
live_question=read('goal1-step1-live-model.json');assert live_question['question_reviewed_during_cooldown']and not live_question['verified_mention_fabricated']and live_question['wechat_sends']==0
proactive=read('goal1-step3-live-model.json');assert {x['is_group']for x in proactive['decisions']}=={True,False}and proactive['wechat_sends']==0
resource=read('goal1-step5-live-resource.json');assert resource['configured_provider_live']and resource['model_selected_real_media']and resource['staging_uploads']==2 and resource['prepared_wav_bytes']>5_000_000 and resource['wechat_sends']==0
downloads=[x for x in resource['activities']if x['tool']=='web_media_download'];assert len(downloads)==2 and all(x['status']=='succeeded'and x['bytes']==3316821 for x in downloads)
live_plan=read('goal1-step6-live-plan.json');assert len(live_plan['decision']['messages'])==2 and live_plan['review']['action']=='cancel'and live_plan['wechat_sends']==0
assert before==[digest(p)for p in private_files]
assert not(workspace/'secrets/ui-qa-auth-state.json').exists()
requirement_text=(workspace/'目标1.md').read_text(encoding='utf-8-sig')
assert all(re.search(r'^'+str(i)+r'\.',requirement_text,re.M)for i in range(7))
items=[
 {'id':0,'requirement':'歌曲分段发送可靠性及模拟录音时长','state':'verified','evidence':['goal1-final-native-tests.log','goal1-voice-prefix-recovery.json'],'proof':'当前对应源码187项核心／138项协议测试通过；原生库保留两段独立接受回执，段间42.188秒；此前修正40秒播放由用户确认，新两段未新增手机播放确认'},
 {'id':1,'requirement':'模型失败诊断恢复、F4V视频、明确问题进入模型判断','state':'verified','tests':coverage[1],'evidence':['goal1-step1-linux-tests.log','goal1-step1-live-model.json'],'proof':'当前真实模型问题判断和FFmpeg多音轨整理通过；原生库中修复视频回执仍accepted；旧网络细因没有记录，未补造'},
 {'id':2,'requirement':'记忆发言者备注／群内昵称／昵称优先并保留唯一身份','state':'verified','tests':coverage[2],'proof':'当前记忆API有可读用户及角色名称，优先级、同名不同人及旧正文／ID保留由专项测试覆盖'},
 {'id':3,'requirement':'群聊与好友分别选择主动问候并可配置','state':'verified','tests':coverage[3],'evidence':['goal1-step3-live-model.json','ui-goal1-step3-regression.json','goal1-final-ui.json'],'proof':'当前选择器与已授权目标一致，独立目标闲置与日预算持久保存；模型可选择安静'},
 {'id':4,'requirement':'冲浪分享目标多选，保留安静时间与次数规则','state':'verified','tests':coverage[4],'evidence':['ui-goal1-step4-surf.json','goal1-final-ui.json'],'proof':'当前网页为命名多选框，API与保存值一致；安静时段不启动或扣次数，随机区间和边界取消有测试'},
 {'id':5,'requirement':'真实搜索下载、错误原因、可取消有界执行、执行前回应','state':'verified','tests':coverage[5],'evidence':['goal1-step5-linux-tests.log','goal1-step5-live-resource.json','goal1-step5-linux-transport.log'],'proof':'修正旧测试夹具账号后实际完整下载两次，模型自主选择真实附件；临时失败有界恢复，永久错误不盲重试；进度与终态独立'},
 {'id':6,'requirement':'参考指定文档的六项真人式连续发言机制','state':'verified','tests':coverage[6],'evidence':['goal1-step6-linux-background.log','goal1-final-linux-conversation.log','goal1-step6-live-plan.json','goal1-final-ui.json'],'proof':'语义计划、连续多条、持续收件、未发计划复核、独立唤醒、参与意愿／长期记忆逐项有执行证据；文件与媒体回执独立，生成与发送分离'},
]
report={'audited_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'objective_file':'目标1.md','objective_sha256':digest(workspace/'目标1.md'),'web_url':base+'/', 'web_sha256':web_hash,'native_sha256':native_hash,'native_online':True,'bot_running':True,'native_test_counts':{'core':187,'protocol':138},'go_test_packages_passed':len(packages),'go_named_tests_passed':len(passed),'current_named_user_turns':named_users,'current_named_assistant_turns':named_assistants,'settings_and_native_keys_preserved':True,'browser_mutations':0,'docker_images_built_or_exported':0,'compiler_container_restored_stopped':True,'new_wechat_audit_sends':0,'items':items,'completed_items':list(range(7)),'remaining_items':[],'overall_goal_complete':True,'limits':['网络来源仍可能超时、限流或改变接口；已实现有界恢复、明确失败与替代来源，不保证所有资源可下载','本次未新增手机端播放／渲染验收；新分段以服务端回执和协议测试证明，先前修正40秒语音已获用户播放确认','微信已接受或已经发生的外部工具操作无法保证撤回；只停止尚未执行部分','QQ接入、插件外部服务账号及生成API按原范围留作可扩展接口，本次不构建或导出镜像']}
(evidence/'goal1-final-acceptance.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps({k:v for k,v in report.items()if k!='items'},ensure_ascii=False))
