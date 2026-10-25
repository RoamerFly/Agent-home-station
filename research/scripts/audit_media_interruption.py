"""Verify deployed recovery/memory changes without sending any new message."""
import hashlib,http.cookiejar,json,pathlib,subprocess,urllib.request
workspace=pathlib.Path(__file__).resolve().parents[2]
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
evidence=workspace/'research/evidence'
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
base='http://127.0.0.1:8081'
with client.open(urllib.request.Request(base+'/api/login',data=json.dumps({'password':(root/'admin/admin-token').read_text().strip()}).encode(),headers={'Origin':base,'Content-Type':'application/json'}),timeout=10)as response:assert response.status==200
def get(route):
    with client.open(base+route,timeout=20)as response:return json.load(response)
status=get('/api/status')
assert status['native']['authenticated']and status['native']['connection']=='online'and status['bot']['state']=='running'
digest=lambda path:hashlib.sha256(path.read_bytes()).hexdigest()
assert digest(root/'bin/wechat-admin')==digest(workspace/'dist/linux-amd64/wechat-admin')
assert digest(root/'bin/wx')==json.loads((evidence/'goal1-native-voice-update.json').read_text())['native_binary_sha256']
for unit in ['wechat-bot-web.service','wechat-bot-native.service']:subprocess.run(['systemctl','--user','is-active','--quiet',unit],check=True)
scopes=get('/api/companion/state')['scopes'];scope=next(x for x in scopes if x['thread']=='50583054216@chatroom')
memory=get('/api/companion/memory/'+scope['id'])
media=[x['media']for x in memory['messages']if x.get('media')and x['media']['job_id']=='job_af5d84ec-41b3-4090-aafe-408cd40b6a69']
assert len(media)==1 and media[0]['state']=='accepted'and media[0]['accepted_parts']==4 and media[0]['total_parts']==4
model=json.loads((evidence/'live-media-reuse.json').read_text());assert model['kind']=='file'and model['tool_calls']==['media_reuse']and model['network_downloads']==0 and model['wechat_sends']==0
ui=json.loads((evidence/'ui-media-memory.json').read_text(encoding='utf-8-sig'));assert ui['scroll_preserved']['before']==ui['scroll_preserved']['after']and ui['log_link']['rows']==1 and all(not x['page_overflow']and not x['media_overflow']for x in ui['checks'])
tests=(evidence/'media-interruption-tests.log').read_text(encoding='utf-8-sig')
for name in ['TestMediaPacingDoesNotBlockStopAndPersistsCancelledMemory','TestMediaRetryUsesOriginalJobPreservesReceiptAndWaits','TestUncertainNativeGuardAndDrainDoNotResubmit','TestUpdateDrainWaitsForMediaWithoutCancellingOrTakingNewInbox','TestCachedSongCanBeReusedAsFileAfterEngineRecreation','TestLegacyVoiceSourceCanBecomeRealWAVFileWithoutNetwork','TestDrainRoutesRequireAuthCSRFTicketAndDoNotCancelWork']:assert '--- PASS: '+name in tests
queue=json.loads((root/'bot-state/messages-v2.json').read_text());tracked=[x for x in queue.get('media',[])if x['id']==media[0]['job_id']];assert len(tracked)==1 and tracked[0]['state']=='accepted'
assert not(workspace/'secrets/ui-qa-auth-state.json').exists()
report={'voice_sending_no_longer_blocks_inbox':True,'explicit_stop_cancels_unsent_parts_and_survives_restart':True,'safe_retry_same_job_after_delay_and_two_checks':True,'native_uncertain_submission_guard_preserved':True,'voice_source_associated_with_conversation_asset':True,'new_downloads_retain_original_audio_bytes':True,'legacy_voice_source_reusable_as_real_wav_file':True,'real_configured_model_reused_without_new_search_or_download':True,'structured_voice_memory_visible':True,'reported_job_accepted_parts':4,'reported_job_total_parts':4,'phone_playback_of_recovered_parts_verified':False,'update_drain_waits_for_active_media_and_preserves_new_inbox':True,'local_service_updated_native_session_unchanged':True,'bot_running':True,'web_url':base,'ui_theme_mobile_log_link_scroll_regressions':'passed','docker_images_built_or_exported':0,'overall_goal_complete':False,'remaining_objective_items':[6]}
(evidence/'media-interruption-acceptance.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8')
print(json.dumps(report,ensure_ascii=False))
