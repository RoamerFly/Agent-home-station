"""Read-only milestone audit. Does not send messages or alter configuration."""
import hashlib
import json
import pathlib
import re
import sqlite3
import subprocess
import urllib.request

workspace = pathlib.Path(__file__).resolve().parents[2]
root = pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
evidence = workspace / 'research/evidence'
song = json.loads((evidence / 'goal1-voice-prefix-recovery.json').read_text())
assert song['state'] == 'accepted' and song['all_notes_have_accepted_receipts']
assert len(song['notes']) == 2 and sum(n['duration_ms'] for n in song['notes']) == 80248
assert song['inter_note_gap_ms'] >= 40924
counts = {}
for name in ('protocol', 'core'):
    output = (evidence / ('goal1-native-' + name + '-tests.log')).read_text()
    matches = re.findall(r'test result: ok\. (\d+) passed; 0 failed', output)
    assert matches, name + ' tests not proven'
    counts[name] = max(map(int, matches))
assert counts == {'protocol': 138, 'core': 186}
assert json.loads((evidence / 'ui-goal1-voice-paced.json').read_text())['media_submissions'] == 0
for unit in ('wechat-bot-native.service', 'wechat-bot-web.service'):
    subprocess.run(['systemctl', '--user', 'is-active', '--quiet', unit], check=True)
client = urllib.request.build_opener(urllib.request.ProxyHandler({}))
with client.open('http://127.0.0.1:8081/healthz', timeout=5) as response:
    assert response.status == 200
digest = lambda path: hashlib.sha256(path.read_bytes()).hexdigest()
assert digest(root / 'bin/wx') == digest(workspace / 'dist/linux-amd64/wx-linux-native')
assert digest(root / 'bin/wechat-admin') == digest(workspace / 'dist/linux-amd64/wechat-admin')
db = sqlite3.connect('file:' + str(root / 'accounts/background-test-20261024/agent/control.sqlite') + '?mode=ro', uri=True)
assert db.execute("select count(*) from media_jobs where state in ('queued','running')").fetchone()[0] == 0
report = {
    'objective_item': 0,
    'implementation_and_backend_acceptance': 'passed',
    'native_tests_passed': counts,
    'full_track_ms': 80248,
    'accepted_voice_notes': 2,
    'inter_note_gap_ms': song['inter_note_gap_ms'],
    'runtime_prefix_retry_exercised': any(song['nonfinal_prefix_retries']),
    'prefix_retry_boundaries_unit_tested': True,
    'final_range_retries': 0,
    'new_notes_phone_playback_verified': False,
    'previous_corrected_40s_phone_playback_verified': True,
    'native_and_web_active_and_match_dist': True,
    'configuration_and_keys_preserved': json.loads((evidence / 'goal1-native-voice-update.json').read_text())['config_and_keys_unchanged'],
    'group_test_sends': 0,
    'docker_images_built_or_exported': 0,
    'remaining_objective_items': [1, 2, 3, 4, 5, 6],
    'overall_goal_complete': False
}
(evidence / 'goal1-step0-acceptance.json').write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding='utf8')
print(json.dumps(report, ensure_ascii=False))
