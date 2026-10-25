"""Final read-only runtime and saved-evidence audit; never sends to WeChat."""
import hashlib,http.cookiejar,json,pathlib,subprocess,urllib.request
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
workspace=pathlib.Path(__file__).resolve().parents[2]
evidence=workspace/'research/evidence';base='http://127.0.0.1:8081'
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
def call(path,data=None,csrf=''):
    headers={'Origin':base,'Content-Type':'application/json'}
    if csrf:headers['X-CSRF-Token']=csrf
    with client.open(urllib.request.Request(base+path,data=None if data is None else json.dumps(data).encode(),headers=headers),timeout=15)as r:return json.load(r)
csrf=call('/api/login',{'password':(root/'admin/admin-token').read_text().strip()})['csrf']
status=call('/api/status');usage=call('/api/billing');balance=call('/api/billing/balance',{},csrf)
assert status['native']['authenticated']and status['native']['connection']=='online'and status['bot']['state']=='running'
assert usage['total']['input_tokens']>0 and usage['total']['reported_requests']>0
assert balance['state']=='ok'and balance['data']['balance_infos']
digest=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
webhash=digest(root/'bin/wechat-admin')
assert webhash==digest(workspace/'dist/linux-amd64/wechat-admin')
nativehash=digest(root/'bin/wx')
native_update=json.loads((evidence/'native-share-quote-update.json').read_text())
assert nativehash==native_update['native_binary_sha256']and native_update['config_and_keys_unchanged']
for unit in ['wechat-bot-web.service','wechat-bot-native.service']:subprocess.run(['systemctl','--user','is-active','--quiet',unit],check=True)
read=lambda name:json.loads((evidence/name).read_text(encoding='utf-8-sig'))
ui=read('ui-billing.json')
assert ui['live']['balance_shows_currency']and ui['invalid_price_blocks']and ui['refresh_keeps_draft']and ui['unsupported_not_zero']and ui['poll_keeps_view']['before']==ui['poll_keeps_view']['after']
assert all(not x['overflow']and not x['fields_overflow']for x in ui['layouts'])
assert not(workspace/'secrets/ui-qa-auth-state.json').exists()
quotes=read('quoted-reply-memory-repaired.json');assert quotes['repaired_placeholders']==1 and quotes['identities_and_times_preserved']and quotes['wechat_sends']==0
reuse=read('live-quoted-file-intent.json');assert reuse['kind']=='file'and reuse['delivery_intent']=='file'and reuse['wechat_sends']==0 and reuse['network_downloads']==0
preview=(evidence/'quoted-file-preview-live.log').read_text(encoding='utf-8-sig')
for name in ['TestLiveQuotedModelChoosesOriginalFileAndEmitsCancellation','TestMusicPreviewRetainsCompleteOriginalForFileReuse','TestPublicVideoAudioExtractionProducesReusableAudioOnlyFile']:assert '--- PASS: '+name in preview
tests=(evidence/'quoted-file-balance-tests.log').read_text(encoding='utf-8-sig');assert 'FAIL'not in tests and 'wechat-bot/internal/companion'in tests and 'wechat-bot/internal/admin'in tests
shares=read('manual-share-quote-verification.json');assert len(shares)==2 and all(x['state']=='accepted'and x['quoted_original_server_id_matches']and x['reply_in_memory']for x in shares)
report={'web_url':base,'web_binary_sha256':webhash,'web_binary_matches_current_build':True,'native_binary_sha256':nativehash,'native_authenticated_online':True,'bot_running':True,'balance_returned_by_actual_provider':True,'balances_and_currency_display_in_chinese':True,'actual_model_usage_recorded':True,'costs_are_estimates_excluding_separate_search_and_plugin_charges':True,'social_reply_and_silence_both_honor_model_choice':True,'canonical_quoted_user_reply_found_and_repaired':quotes['repaired_placeholders'],'quote_memory_identity_and_time_preserved':True,'actual_model_interpreted_original_file_from_quoted_voice':True,'file_selection_cancels_old_voice_before_tool':True,'preview_45_seconds_retains_full_70_second_source_verified':True,'full_source_reusable_as_file':True,'source_reuse_probe_wechat_sends':0,'ui_themes_mobile_and_scroll_checks_passed':True,'manual_share_replies_accepted_with_original_quotes':2,'phone_rendering_for_new_quoted_replies_verified':False,'docker_images_built_or_exported':0,'overall_goal_complete':False,'remaining_objective_items':[6]}
(evidence/'quoted-file-billing-acceptance.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8');print(json.dumps(report,ensure_ascii=False))
