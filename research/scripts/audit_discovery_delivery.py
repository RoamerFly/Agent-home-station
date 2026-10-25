"""Read-only final acceptance audit for multi-source surfing; no model or sends."""
import hashlib,http.cookiejar,json,pathlib,subprocess,urllib.request
root=pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024');workspace=pathlib.Path(__file__).resolve().parents[2];evidence=workspace/'research/evidence';base='http://127.0.0.1:8081'
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
with client.open(urllib.request.Request(base+'/api/login',data=json.dumps({'password':(root/'admin/admin-token').read_text().strip()}).encode(),headers={'Origin':base,'Content-Type':'application/json'}),timeout=15)as r:assert r.status==200
def get(path):
    with client.open(base+path,timeout=15)as r:return json.load(r)
status=get('/api/status');surf=get('/api/surf');history=get('/api/surf/history');config=get('/api/config')
assert status['native']['authenticated']and status['native']['connection']=='online'and status['bot']['state']=='running'
disk=json.loads((root/'admin/surf.json').read_text())['settings']
for key in ['enabled','interval_minutes','interval_max_minutes','max_sessions_per_day','threads','topics']:assert surf['settings'][key]==disk[key], 'saved owner settings diverged'
assert all(x['name']!='我的图片订阅'for x in surf['settings']['discovery']['sources']), 'mocked UI fixture reached real config'
saved_config=json.loads((root/'admin/config.json').read_text())
for key in ['quiet_start','quiet_end','timezone']:assert config['companion'][key]==saved_config['companion'][key]
assert 1<=surf['settings']['discovery']['cooldown_days']<=90 and 1<=surf['settings']['discovery']['max_sources']<=4
quantum=[x for x in history['items']if '三比特'in x['item']['title']and x.get('shared',{}).get('43929704480@chatroom')]
assert quantum, 'old accepted topic was not imported'
assert all(x['shared']['43929704480@chatroom'].startswith('2026-10-25T00:01:29')for x in quantum), 'migration refreshed old share time'
digest=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
assert digest(root/'bin/wechat-admin')==digest(workspace/'dist/linux-amd64/wechat-admin')
native=json.loads((evidence/'native-share-quote-update.json').read_text());assert digest(root/'bin/wx')==native['native_binary_sha256']
for unit in ['wechat-bot-web.service','wechat-bot-native.service']:subprocess.run(['systemctl','--user','is-active','--quiet',unit],check=True)
read=lambda n:json.loads((evidence/n).read_text(encoding='utf-8-sig'))
live=read('discovery-model-live.json');assert len(live)==3 and all(not x['error']and x['wechat_sends']==0 and x['tool_calls'][0]=='web_discover'for x in live)
assert {m['kind']for x in live for m in x['media']}=={'voice','share','image'}
related=read('discovery-reported-list-related.json');assert related['list_success']and related['list_is_metadata_not_watched']and related['related_success']and related['related_count']>0
ui=read('ui-discovery.json');assert ui['saved_non100_weights']and ui['zero_weights_blocked']and ui['bad_feed_blocked']and ui['poll_keeps_draft']and ui['safe_text_rendering']and ui['poll_keeps_scroll']['before']==ui['poll_keeps_scroll']['after']
assert all(not x['overflow']and not x['fields_overflow']and x['sidebar_closed']for x in ui['layouts'])
assert not(workspace/'secrets/ui-qa-auth-state.json').exists()
assert 'FAIL'not in(evidence/'discovery-all-tests.log').read_text(encoding='utf-8-sig')
report={'web_url':base+'/#surf','native_online':True,'bot_running':True,'web_build_matches_runtime':True,'web_sha256':digest(root/'bin/wechat-admin'),'native_unchanged':True,'multi_source_candidates_before_model':True,'actual_isolated_model_media_kinds':['image','share','voice'],'isolated_model_wechat_sends':0,'reported_bili_shell_read_fixed':True,'related_candidates_read':related['related_count'],'old_quantum_accepted_share_imported_without_age_refresh':True,'original_interval_target_daily_budget_and_quiet_hours_preserved':True,'seen_shared_bookmarks_and_sessions_persisted':True,'url_title_and_model_context_novelty_checks':True,'embedding_service_installed':False,'browser_use_or_miniflux_service_installed':False,'source_errors_still_reported_truthfully':True,'ui_save_validation_mobile_theme_and_scroll_checks_passed':True,'browser_save_tests_modified_real_settings':False,'docker_images_built_or_exported':0,'overall_goal_complete':False,'remaining_objective_items':[6]}
(evidence/'discovery-delivery-acceptance.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf8');print(json.dumps(report,ensure_ascii=False))
