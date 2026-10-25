"""Local validation client for the source-built Linux native daemon.

Run under WSL/Linux. Reads the daemon token locally; never emits it or account
credentials. The only send supported by this helper is the user-approved single
File Transfer text. A durable pre-request marker prevents a second submission.
"""
import argparse
import json
import os
import pathlib
import time
import urllib.error
import urllib.request
import uuid

parser = argparse.ArgumentParser()
parser.add_argument('command', choices=['setup', 'login', 'watch-login', 'status', 'send-test', 'watch-send'])
parser.add_argument('--data-root', required=True)
parser.add_argument('--qr-output')
args = parser.parse_args()
root = pathlib.Path(args.data_root)
assert root.is_absolute() and root.is_dir()
token = (root / 'api-token').read_text().strip()
assert 24 <= len(token) <= 1024
account = 'background-test-20261024'
base = 'http://127.0.0.1:6174'


def request(path, data=None, key=None, image=False):
    headers = {'Authorization': 'Bearer ' + token}
    if data is not None: headers['Content-Type'] = 'application/json'
    if key: headers['Idempotency-Key'] = key
    raw = None if data is None else json.dumps(data).encode()
    req = urllib.request.Request(base + path, data=raw, headers=headers)
    # Direct loopback only, with no redirects or environment proxy.
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *a, **kw): return None
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    with opener.open(req, timeout=30) as r:
        b = r.read((4 << 20) + 1)
        if len(b) > 4 << 20: raise ValueError('local response bound')
        if image:
            assert r.headers.get_content_type() == 'image/png' and b.startswith(b'\x89PNG\r\n\x1a\n')
            return b
        return json.loads(b)


def new_marker(path, data):
    with path.open('x', encoding='utf-8') as f:
        os.chmod(path, 0o600)
        json.dump(data, f)
        f.flush()
        os.fsync(f.fileno())


def update(path, data):
    temp = path.with_suffix('.temporary')
    with temp.open('w', encoding='utf-8') as f:
        os.chmod(temp, 0o600)
        json.dump(data, f)
        f.flush()
        os.fsync(f.fileno())
    os.replace(temp, path)


prefix = '/v1/accounts/' + account
try:
    if args.command == 'setup':
        result = request('/v1/accounts', {'id': account, 'alias': 'background-test'})
        print(json.dumps({'account_created': result.get('id') == account}), flush=True)
    elif args.command == 'status':
        result = request(prefix)
        print(json.dumps({k: result.get(k) for k in ['authenticated', 'connected', 'state', 'desired', 'profileProjection']}), flush=True)
    elif args.command == 'login':
        marker = root / 'validation-login.json'
        record = {'started_at': int(time.time()), 'key': uuid.uuid4().hex, 'account': account}
        new_marker(marker, record)
        result = request(prefix + '/auth/login', {'strategy': 'qr', 'forceQr': True, 'allowQr': True, 'timeoutSeconds': 300, 'computerName': 'WechatBot', 'qrRefresh': {'mode': 'manual', 'maxRefreshes': 0}}, record['key'])
        record['operation'] = result
        update(marker, record)
        print(json.dumps({'login_operation_started': True, 'state': result.get('state')}), flush=True)
    elif args.command == 'watch-login':
        assert args.qr_output, '--qr-output is required'
        marker = root / 'validation-login.json'
        record = json.loads(marker.read_text())
        opid = record['operation']['id']
        last, saved = None, False
        for _ in range(170):
            op = request(prefix + '/operations/' + opid)
            record['operation'] = op
            update(marker, record)
            state = op['state']
            result = op.get('result', {})
            if state != last:
                print(json.dumps({'login_state': state, 'code': result.get('code'), 'service_code': result.get('serviceCode')}), flush=True)
                last = state
            if not saved and result.get('qrAvailable'):
                png = request(prefix + '/operations/' + opid + '/qr?generation=' + str(result['qrGeneration']), image=True)
                path = pathlib.Path(args.qr_output)
                with path.open('xb') as f: f.write(png)
                saved = True
                print(json.dumps({'qr_saved': str(path)}), flush=True)
            if state in ['succeeded', 'failed', 'cancelled', 'expired', 'rejected', 'needs_login', 'approval_required', 'challenge_required']:
                print(json.dumps({'account_authorization_succeeded': state == 'succeeded', 'real_message_sent': False}), flush=True)
                break
            time.sleep(2)
        else: print(json.dumps({'local_watch_expired': True}), flush=True)
    elif args.command in ['send-test', 'watch-send']:
        marker = root / 'validation-send.json'
        if args.command == 'send-test':
            record = {'started_at': int(time.time()), 'key': uuid.uuid4().hex, 'destination': 'filehelper', 'text': '后台发送测试'}
            new_marker(marker, record)
            result = request(prefix + '/sends', {'kind': 'text', 'threadId': 'filehelper', 'text': record['text']}, record['key'])
            record['result'] = result
            update(marker, record)
        else:
            record = json.loads(marker.read_text())
            result = record['result']
        sendid = result['id']
        for _ in range(30):
            result = request(prefix + '/sends/' + sendid)
            record['result'] = result
            update(marker, record)
            if result['state'] not in ['queued', 'sending', 'submitting']: break
            time.sleep(1)
        accepted = result['state'] == 'accepted' and result['destination'] == 'filehelper' and str(result.get('serverId', '')).isdigit() and int(result['serverId']) > 0 and str(result.get('clientId', '')).isdigit() and int(result['clientId']) > 0
        print(json.dumps({'server_accepted': accepted, 'state': result['state'], 'destination': result['destination'], 'client_id': result.get('clientId'), 'server_id': result.get('serverId'), 'error_code': result.get('errorCode'), 'automatically_retried': False}), flush=True)
except FileExistsError:
    raise SystemExit('checkpoint already exists; do not resubmit; inspect the saved operation')
except urllib.error.HTTPError as e:
    # Preserve the exact response privately, while keeping account text out of logs.
    error = e.read(16384)
    update(root / 'validation-http-error.json', {'http_status': e.code, 'body': error.decode('utf-8', 'replace')})
    raise SystemExit('local native API returned HTTP ' + str(e.code) + '; private diagnostic saved; not retried')
