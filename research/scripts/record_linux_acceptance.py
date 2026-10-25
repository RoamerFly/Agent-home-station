"""Read-only acceptance capture; never creates another send intent."""
import argparse
from datetime import datetime, timezone
import json
from pathlib import Path
import urllib.request
import urllib.error

parser = argparse.ArgumentParser()
parser.add_argument('--data-root', required=True)
parser.add_argument('--phone-confirmed', action='store_true', required=True)
args = parser.parse_args()
root = Path(args.data_root)
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
def get(url, token=None):
    headers = {} if token is None else {'Authorization': 'Bearer ' + token}
    with opener.open(urllib.request.Request(url, headers=headers), timeout=10) as response:
        return response.status, json.load(response)

token = (root / 'api-token').read_text().strip()
checkpoint = json.loads((root / 'validation-send.json').read_text())
prefix = 'http://127.0.0.1:6174/v1/accounts/background-test-20261024'
_, account = get(prefix, token)
_, receipt = get(prefix + '/sends/' + checkpoint['result']['id'], token)
assert account['authenticated'] is True
assert receipt['state'] == 'accepted' and receipt['destination'] == 'filehelper'
assert receipt['clientId'] == '788294973' and receipt['serverId'] == '6809579812965061880'
health = get('http://127.0.0.1:8787/healthz')
ready = get('http://127.0.0.1:8787/readyz')
assert health[0] == ready[0] == 200
report = {
    'recorded_at_utc': datetime.now(timezone.utc).isoformat(),
    'backend': 'thisnick/agent-wechat Linux native Rust source',
    'commit': '7095ef01e0cd3d5b567bcb6970f379dcd88bbfa8',
    'execution': 'Ubuntu-New WSL2, Linux x86_64 binary extracted from source-built Docker image',
    'official_desktop_process_required': False,
    'original_closed_service_required': False,
    'real_account_authorized': True,
    'destination': 'filehelper',
    'text': checkpoint['text'],
    'send_state': receipt['state'],
    'client_message_id': receipt['clientId'],
    'server_message_id': receipt['serverId'],
    'post_submissions': 1,
    'automatically_retried': False,
    'user_confirmed_phone_receipt': args.phone_confirmed,
    'user_confirmation_text': '已看到“后台发送测试”',
    'go_api_health_http': health[0],
    'go_api_ready_http': ready[0],
    'go_api_real_send_exercised': False,
    'go_api_note': 'Real message sent once via native API; Go adapter verified by authenticated readiness and isolated queued/submitting/accepted tests. No duplicate live send.',
    'live_docker_authorization_exercised': False,
    'cloud_server_exercised': False,
}
output = Path(__file__).resolve().parents[1] / 'evidence/linux-native-acceptance.json'
output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
print(json.dumps({'acceptance_saved': True, 'server_accepted': True, 'phone_receipt_confirmed': True, 'go_api_ready': True}))
