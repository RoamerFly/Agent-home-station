"""Run the local Go API beside an already authorized Linux-native daemon.

Linux/WSL only. No token is printed or placed in process arguments.
"""
import argparse
import os
from pathlib import Path
import secrets

parser = argparse.ArgumentParser()
parser.add_argument('--data-root', required=True)
parser.add_argument('--account', required=True)
args = parser.parse_args()
root = Path(args.data_root).resolve(strict=True)
if not root.is_dir() or root.stat().st_uid != os.getuid() or root.stat().st_mode & 0o077:
    raise SystemExit('data root must be a private directory owned by this user')
token_file = root / 'send-api-token'
if not token_file.exists():
    fd = os.open(token_file, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as f:
        f.write(secrets.token_hex(32) + '\n')
        f.flush()
        os.fsync(f.fileno())
if token_file.stat().st_uid != os.getuid() or token_file.stat().st_mode & 0o077:
    raise SystemExit('sending API token file must be private')
env = dict(os.environ)
env.update({
    'WECHAT_API_TOKEN': token_file.read_text().strip(),
    'WECHAT_ALLOWED_RECIPIENTS': 'filehelper',
    'WECHAT_LISTEN': '127.0.0.1:8787',
    'WECHAT_TRANSPORT': 'linux-native',
    'WECHAT_NATIVE_URL': 'http://127.0.0.1:6174',
    'WECHAT_NATIVE_TOKEN_FILE': str(root / 'api-token'),
    'WECHAT_NATIVE_ACCOUNT': args.account,
})
binary = Path(__file__).resolve().parents[2] / 'dist/linux-amd64/wechat-sendd'
os.execve(binary, [str(binary)], env)
