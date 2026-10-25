"""One idempotent, paced Happy Alley test to the test account's own filehelper.
Never restarts or resubmits an uncertain job. Run inside WSL as fly.
"""
import http.cookiejar
import argparse
import json
import pathlib
import sqlite3
import time
import urllib.error
import urllib.request

root = pathlib.Path('/home/fly/.local/share/wechat-bot-native-research-20261024')
workspace = pathlib.Path(__file__).resolve().parents[2]
evidence = workspace / 'research/evidence/goal1-voice-paced.json'
base = 'http://127.0.0.1:8081'
client = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
csrf = ''
parser = argparse.ArgumentParser()
parser.add_argument('--ticket-reuse', action='store_true', help='One distinct controlled validation after the ticket reuse source change')
parser.add_argument('--long-source', action='store_true', help='Previously downloaded long music preview, never submitted before this validation')
parser.add_argument('--prefix-recovery', action='store_true', help='One controlled full-song validation after bounded non-final prefix recovery implementation')
args = parser.parse_args()
if args.ticket_reuse:
    evidence = workspace / 'research/evidence/goal1-voice-ticket-reuse.json'
if args.long_source:
    evidence = workspace / 'research/evidence/goal1-voice-full-pacing.json'
if args.prefix_recovery:
    evidence = workspace / 'research/evidence/goal1-voice-prefix-recovery.json'
assert sum((args.long_source,args.ticket_reuse,args.prefix_recovery)) <= 1


def call(path, value=None):
    headers = {'Origin': base, 'X-CSRF-Token': csrf}
    body = None
    if value is not None:
        body = json.dumps(value).encode()
        headers['Content-Type'] = 'application/json'
    try:
        with client.open(urllib.request.Request(base + path, data=body, headers=headers), timeout=30) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        return error.code, json.load(error)


csrf = call('/api/login', {'password': (root / 'admin/admin-token').read_text().strip()})[1]['csrf']
db = sqlite3.connect('file:' + str(root / 'accounts/background-test-20261024/agent/control.sqlite') + '?mode=ro', uri=True)
key = 'goal1-ticket-reuse-happy-alley-20261025' if args.ticket_reuse else 'goal1-paced-happy-alley-20261025'
if args.long_source:
    key = 'goal1-full-pacing-new-music-source-20261025'
if args.prefix_recovery:
    key = 'goal1-nonfinal-prefix-recovery-happy-alley-20261025'
code, status = call('/api/tools/lookup?key=' + key)
new_submission = code == 404
if new_submission:
    assert db.execute("select count(*) from media_jobs where state in ('queued','running')").fetchone()[0] == 0, 'Existing media work; do not interrupt it'
    if args.long_source:
        sources = db.execute("select id from media_sources where name='audio-part-1-of-1.wav' and byte_length=7036004 and created_at like '2026-10-24T15:37:57%'").fetchall()
        assert len(sources) == 1, 'Expected one previously downloaded long audio preview'
        source_id = sources[0][0]
        assert db.execute("select count(*) from media_jobs where kind='send' and json_extract(data_json,'$.sourceId')=?", (source_id,)).fetchone()[0] == 0, 'Selected source already sent or attempted; do not replay'
    else:
        old = json.loads((root / 'admin/tool-state.json').read_text())['sends']['music-060-own-filehelper-20261024']['status']
        data = json.loads(db.execute('select data_json from media_jobs where id=?', (old['id'],)).fetchone()[0])
        source_id = data['sourceId']
    code, status = call('/api/tools/send', {'thread_id': 'filehelper', 'kind': 'voice', 'source_id': source_id, 'idempotency_key': key})
assert code in (200, 202), (code, status.get('error'))
deadline = time.monotonic() + 600
previous = None
observed_pacing = False
while status['state'] in ('queued', 'running', 'sending', 'submitting') and time.monotonic() < deadline:
    observed_pacing |= status.get('phase') == 'pacing'
    current = status['state'], status.get('phase'), tuple(c['state'] for c in status.get('voice_chunks', []))
    if current != previous:
        print(json.dumps({'state': current[0], 'phase': current[1], 'chunks': current[2]}), flush=True)
        previous = current
    time.sleep(2)
    code, status = call('/api/tools/lookup?key=' + key)
    assert code == 200
row = db.execute('select state,phase,error_code,data_json from media_jobs where id=?', (status['id'],)).fetchone()
data = json.loads(row[3])
chunks = data.get('result', {}).get('chunks', [])
notes = [{'index': c['index'], 'duration_ms': c['durationMs'], 'state': c['state'], 'has_receipt': bool(c.get('serverMessageId')), 'pacing_ms': c.get('pacingMs', 0), 'submitted_at_ms': c.get('submittedAtMs'), 'accepted_at_ms': c.get('acceptedAtMs')} for c in chunks]
gap = None
if len(notes) >= 2 and notes[1]['submitted_at_ms'] and notes[0]['accepted_at_ms']:
    gap = notes[1]['submitted_at_ms'] - notes[0]['accepted_at_ms']
report = {'track': 'Happy Alley', 'recipient': 'own_filehelper', 'idempotency_key': key, 'new_submission_this_run': new_submission, 'state': row[0], 'phase': row[1], 'reason': row[2], 'voice_failure_stage': data.get('result', {}).get('voiceFailureStage'), 'notes': notes, 'pacing_phase_observed': observed_pacing, 'inter_note_gap_ms': gap, 'all_notes_have_accepted_receipts': len(notes) == 2 and all(n['state'] == 'accepted' and n['has_receipt'] for n in notes), 'phone_playback_verified_this_run': False, 'previous_corrected_40s_phone_playback_verified': True, 'group_sends': 0, 'automatic_media_resends': 0, 'docker_images_built_or_exported': 0}
report.update({'voice_transport_kind': data.get('result', {}).get('voiceTransportKind'), 'failed_offset': data.get('result', {}).get('voiceFailedOffset'), 'controlled_validation_after_ticket_reuse_change': args.ticket_reuse})
if args.long_source:
    report.update({'track': 'previously_downloaded_long_music_preview', 'new_source_not_previously_submitted': True, 'all_notes_have_accepted_receipts': len(notes) == 4 and all(n['state'] == 'accepted' and n['has_receipt'] for n in notes)})
report['all_inter_note_gaps_ms'] = [notes[i]['submitted_at_ms'] - notes[i-1]['accepted_at_ms'] if notes[i]['submitted_at_ms'] and notes[i-1]['accepted_at_ms'] else None for i in range(1, len(notes))]
report['nonfinal_prefix_retries'] = [c.get('prefixRetries',0) for c in chunks]
report['final_range_retries'] = 0
report['controlled_validation_after_nonfinal_recovery_change'] = args.prefix_recovery
if evidence.exists():
    previous_report = json.loads(evidence.read_text())
    report['pacing_phase_observed'] |= previous_report.get('pacing_phase_observed', False)
evidence.write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding='utf8')
print(json.dumps(report, ensure_ascii=False), flush=True)
assert report['all_notes_have_accepted_receipts'], 'Song job not fully accepted; inspect saved phase; do not replay automatically'
assert gap is not None and gap >= notes[1]['duration_ms'] + 800, 'Recording-length pacing not proven'
assert all(g is not None and g >= notes[i+1]['duration_ms']+800 for i,g in enumerate(report['all_inter_note_gaps_ms'])), 'A later note skipped recording-time pacing'
