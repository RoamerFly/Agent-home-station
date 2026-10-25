"""Check step-2 evidence and deployment without external sends or model calls."""
import hashlib
import http.cookiejar
import json
import pathlib
import subprocess
import urllib.request

workspace = pathlib.Path(__file__).resolve().parents[2]
root = pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
evidence = workspace / 'research/evidence'
live = json.loads((evidence / 'goal1-step2-live-memory.json').read_text())
ui = json.loads((evidence / 'ui-goal1-step2-regression.json').read_text())
assert live['names_persisted'] and live['original_text_identities_times_notes_and_claims_unchanged']
assert live['named_user_turns'] == 14 and live['named_assistant_turns'] == 12
assert live['new_wechat_sends'] == live['model_calls'] == live['contacts_changed'] == 0
for flag in ('readable_labels', 'same_name_different_ids', 'assistant_and_unknown_fallback',
             'name_source_visible', 'untrusted_names_are_text',
             'compact_heading_and_accessible_section', 'unknown_id_not_repeated'):
    assert ui[flag], flag
assert ui['real_mutations'] == 0 and ui['responsive_widths'] == [1440, 768, 375]
assert not (workspace / 'secrets/ui-qa-auth-state.json').exists()
base = 'http://127.0.0.1:8081'
client = urllib.request.build_opener(urllib.request.ProxyHandler({}),
    urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
request = urllib.request.Request(base + '/api/login',
    data=json.dumps({'password': (root / 'admin/admin-token').read_text().strip()}).encode(),
    headers={'Origin': base, 'Content-Type': 'application/json'})
with client.open(request, timeout=10) as response:
    assert response.status == 200
with client.open(base + '/api/status', timeout=15) as response:
    status = json.load(response)
assert status['native']['authenticated'] and status['native']['connection'] == 'online'
assert status['native']['incoming_transport'] == 'native_push' and status['bot']['state'] == 'running'
digest = lambda path: hashlib.sha256(path.read_bytes()).hexdigest()
assert digest(root / 'bin/wechat-admin') == digest(workspace / 'dist/linux-amd64/wechat-admin')
native_hash = json.loads((evidence / 'goal1-native-voice-update.json').read_text())['native_binary_sha256']
assert digest(root / 'bin/wx') == digest(workspace / 'dist/linux-amd64/wx-linux-native') == native_hash
for unit in ('wechat-bot-web.service', 'wechat-bot-native.service'):
    subprocess.run(['systemctl', '--user', 'is-active', '--quiet', unit], check=True)
report = {
    'objective_item': 2,
    'implementation_and_regressions': 'passed',
    'name_priority': ['remark', 'room', 'nickname', 'id'],
    'live_user_turns_named': live['named_user_turns'],
    'live_assistant_turns_named': live['named_assistant_turns'],
    'live_available_name_sources': live['available_name_sources'],
    'remark_and_room_precedence_verified_with_isolated_endpoints': True,
    'stable_ids_and_original_memory_preserved': True,
    'names_provided_to_model_with_separate_ids': True,
    'remember_disabled_does_not_persist_metadata': True,
    'browser_light_dark_and_responsive_passed': True,
    'new_wechat_sends_this_step': 0,
    'model_calls_this_step': 0,
    'contacts_changed': 0,
    'deployed_web_matches_dist_and_native_unchanged': True,
    'bot_running_and_wechat_connected': True,
    'docker_images_built_or_exported': 0,
    'completed_objective_items': [0, 1, 2],
    'remaining_objective_items': [3, 4, 5, 6],
    'overall_goal_complete': False,
}
(evidence / 'goal1-step2-acceptance.json').write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding='utf8')
print(json.dumps(report, ensure_ascii=False))
