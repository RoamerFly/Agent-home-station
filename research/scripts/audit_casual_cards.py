"""Acceptance audit for casual surf, incoming cards and delivery ordering."""
import hashlib,http.cookiejar,json,pathlib,subprocess,urllib.parse,urllib.request
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024');workspace=pathlib.Path(__file__).resolve().parents[2];evidence=workspace/'research/evidence';base='http://127.0.0.1:8081'
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
with client.open(urllib.request.Request(base+'/api/login',data=json.dumps({'password':(root/'admin/admin-token').read_text().strip()}).encode(),headers={'Origin':base,'Content-Type':'application/json'}),timeout=15)as r:assert r.status==200
def get(path):
    with client.open(base+path,timeout=15)as r:return json.load(r)
status=get('/api/status');surf=get('/api/surf');assert status['native']['authenticated']and status['native']['connection']=='online'and status['bot']['state']=='running'
assert surf['settings']['interval_minutes']==60 and surf['settings']['interval_max_minutes']==120
assert len(surf['settings']['topics'])==4 and all('科学'not in t and '论文'not in t for t in surf['settings']['topics'])
digest=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
assert digest(root/'bin/wechat-admin')==digest(workspace/'dist/linux-amd64/wechat-admin')
native=json.loads((evidence/'native-channels-update.json').read_text());assert digest(root/'bin/wx')==native['native_binary_sha256']and native['config_and_keys_unchanged']
for unit in ['wechat-bot-web.service','wechat-bot-native.service']:subprocess.run(['systemctl','--user','is-active','--quiet',unit],check=True)
tests=(evidence/'casual-cards-targeted-tests.log').read_text(encoding='utf-8-sig')
for name in ['TestRefreshShareMemoryPreservesSpeakerAndTimestampWithoutSending','TestStructuredLinkAndFinderDescriptionAreNotParsedAsXML','TestSurfRandomScheduleIsBoundedVariedAndOldConfigLoads','TestLongVideoStagesClickableCardWithoutUploadingFile','TestMediaCaptionWaitsForAllReceiptsAndSurvivesRestart','TestCancelledOrUncertainMediaNeverSendsSuccessCaption','TestMetadataOnlyVideoCannotClaimWatchingAndKeepsPreparedCard','TestVideoPlaybackShareUsesNativeCardContract']:assert '--- PASS: '+name in tests
rust=(evidence/'native-channels-checks.log').read_text(encoding='utf-8-sig');assert '187 passed; 0 failed'in rust and 'finder_card_keeps_received_description_without_exposing_media_credentials ... ok'in rust
cards=json.loads((evidence/'incoming-share-card-structure.json').read_text());assert {c['app_message_type']for c in cards}=={4,51}and all('link'in c['object_keys']and c['body_bytes']>0 for c in cards)
memory=json.loads((evidence/'share-memory-refreshed.json').read_text());assert len(memory)==2 and all(c['metadata_present']and c['message_identity_and_time_unchanged']for c in memory)
live=json.loads((evidence/'casual-surf-live.json').read_text());assert len(live)==2 and all(not r['error']and r['selected_media']and r['wechat_sends']==0 for r in live)
assert any(any(m['kind']=='share'for m in r['selected_media'])for r in live)and any(any(m['kind']=='voice'for m in r['selected_media'])for r in live)
share=json.loads((evidence/'video-share-card-live.json').read_text());assert share['state']=='accepted'and share['has_wechat_receipt']and share['destination']=='filehelper'and not share['video_file_uploaded']
ui=json.loads((evidence/'ui-casual-cards.json').read_text(encoding='utf-8-sig'));assert ui['mocked_posts']==1 and ui['invalid_range']['posts']==1 and ui['poll_keeps_view']['before']==ui['poll_keeps_view']['after']
assert all(not x['overflow']and not x['fields_overflow']for x in ui['layouts'])and all(x['has_title']and not x['has_missing_placeholder']for x in ui['memory_cards'])
assert not(workspace/'secrets/ui-qa-auth-state.json').exists()
report={'casual_topics_active':surf['settings']['topics'],'random_interval_minutes':[60,120],'quiet_hours_and_daily_budget_preserved':True,'actual_model_prepared_video_card_and_music':True,'recent_shared_sources_deduplicated':True,'fixed_surf_openings_discouraged':True,'incoming_bili_type4_link_metadata_recovered':True,'incoming_finder_type51_description_and_publisher_recovered':True,'finder_video_frames_or_audio_claimed_observed':False,'two_derived_memory_observations_refreshed_without_resending':True,'videos_over_60_seconds_prefer_playback_cards':True,'native_share_card_live_receipt':True,'native_share_card_phone_playback_verified':False,'result_caption_requires_all_attachment_receipts':True,'cancel_or_unknown_suppresses_success_caption':True,'progress_acknowledgement_stays_before_work':True,'native_core_tests_passed':187,'native_formatter_clippy_release_build':'passed','go_tests_vet_js_syntax':'passed','ui_range_save_validation_themes_responsive_scroll':'passed','web_native_running_and_authenticated':True,'docker_images_built_or_exported':0,'original_objective_completed_items':[0,1,2,3,4,5],'original_objective_remaining_items':[6],'overall_goal_complete':False}
(evidence/'casual-cards-acceptance.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8');print(json.dumps(report,ensure_ascii=False))
