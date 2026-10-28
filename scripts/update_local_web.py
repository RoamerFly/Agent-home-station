"""Update only the local web executable, preserving the native session/config.
Run inside Ubuntu-New as fly. Does not build or export a Docker image.
"""
from pathlib import Path
import argparse
import math
import hashlib
import http.cookiejar
import json
import os
import shutil
import subprocess
import tempfile
import time
import urllib.request
import urllib.error
import sys

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--drain-timeout-seconds',type=float,default=15,
                    help='Wait for in-flight work; 0 waits until drained, preserving cancellation and lease renewal')
args=parser.parse_args()
if args.drain_timeout_seconds<0 or not math.isfinite(args.drain_timeout_seconds):
    parser.error('drain timeout must be finite and nonnegative')

workspace = Path(__file__).resolve().parents[1]
deployment_file = workspace / "secrets/local-deployment.json"
deployment = json.loads(deployment_file.read_text(encoding="utf-8-sig")) if deployment_file.exists() else {}
root = Path(deployment.get("data_root", "/home/fly/.local/share/wechat-bot-native-research-20261024"))
assert root.is_dir() and root.stat().st_uid == os.getuid() and not root.stat().st_mode & 0o077
binary = root / "bin/wechat-admin"
source = workspace / "dist/linux-amd64/wechat-admin"
assert source.is_file() and source.read_bytes()[:4] == b"\x7fELF"
config = root / "admin/config.json"
before = hashlib.sha256(config.read_bytes()).hexdigest()
# Drain through the running worker. Uploads, recording pacing and model calls
# finish naturally; newly received messages stay in the persistent inbox.
base = "http://127.0.0.1:8081"
jar = http.cookiejar.CookieJar()
client = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(jar))
def request(route, body=None, csrf=""):
    headers = {"Origin": base, "Content-Type": "application/json"}
    if csrf:
        headers["X-CSRF-Token"] = csrf
    req = urllib.request.Request(base + route, data=None if body is None else json.dumps(body).encode(), headers=headers)
    with client.open(req, timeout=10) as response:
        return json.load(response)
credential = root / "admin/admin-password"
if not credential.exists():
    credential = root / "admin/admin-token"
login = request("/api/login", {"password": credential.read_text().strip()})
try:
    ticket = request("/api/runtime/drain", {}, login["csrf"])["ticket"]
except urllib.error.HTTPError as exc:
    if exc.code != 404:
        raise
    # One-time migration from a pre-drain executable. Refuse to interrupt any
    # observed native job or unfinished AI operation; never blindly restart.
    import sqlite3
    db = sqlite3.connect("file:" + str(root / "accounts/background-test-20261024/agent/control.sqlite") + "?mode=ro", uri=True)
    active = db.execute("select count(*) from media_jobs where state in ('queued','running')").fetchone()[0]
    db.close()
    queue_path = root / "bot-state/messages-v2.json"
    queue = json.loads(queue_path.read_text()) if queue_path.exists() else {}
    busy = any(x.get("phase") in ("processing", "tool_running", "sending") for x in queue.get("inbox", []))
    assert active == 0 and not busy, "Existing worker is busy; update refused so active work is preserved"
    print("One-time migration preflight: no active native job or unfinished AI task.", flush=True)
else:
    import atexit
    def release_drain():
        try:
            request("/api/runtime/undrain", {"ticket": ticket}, login["csrf"])
        except Exception:
            pass  # Restarted services invalidate the old session/ticket.
    atexit.register(release_drain)
    from urllib.parse import quote
    # A busy voice can legitimately need minutes. Do not pause all new chats
    # for that duration merely to update the UI; release and try another time.
    deadline=time.monotonic()+args.drain_timeout_seconds if args.drain_timeout_seconds else None
    attempt=0
    while True:
        if request("/api/runtime/drain?ticket=" + quote(ticket))["ready"]:
            break
        if deadline is not None and time.monotonic()>=deadline:
            raise RuntimeError("Busy worker: update deferred after requested wait; incoming processing resumed, no executable replaced")
        if attempt % 30 == 0:
            print("Waiting for current work to finish before updating (no cancellation).", flush=True)
        attempt+=1
        time.sleep(1)
assert hashlib.sha256(config.read_bytes()).hexdigest() == before, "Configuration changed while preparing update; aborted"
old = hashlib.sha256(binary.read_bytes()).hexdigest()
backup = root / "bin" / ("wechat-admin.previous-" + old[:12])
if not backup.exists():
    shutil.copyfile(binary, backup)
    backup.chmod(0o700)
with tempfile.NamedTemporaryFile(dir=binary.parent, delete=False) as target:
    pending = Path(target.name)
    os.fchmod(target.fileno(), 0o700)
    target.write(source.read_bytes())
    target.flush()
    os.fsync(target.fileno())
pending.replace(binary)
# Workspace mounts contain projects and private session files, never this
# control-plane directory. Persist the explicit root across normal restarts.
dropin=Path.home()/".config/systemd/user/wechat-bot-web.service.d/managed-workspace.conf"
dropin.parent.mkdir(parents=True,exist_ok=True)
value=str(workspace)
assert "\n" not in value and '"' not in value
text='[Service]\nEnvironment="WECHAT_WORKSPACE_ROOT='+value+'"\n'
if not dropin.exists() or dropin.read_text()!=text:
    dropin.write_text(text)
    subprocess.run(["systemctl","--user","daemon-reload"],check=True)
subprocess.run(["systemctl", "--user", "restart", "wechat-bot-web.service"], check=True)
for attempt in range(20):
    try:
        response = urllib.request.build_opener(urllib.request.ProxyHandler({})).open("http://127.0.0.1:8081/healthz", timeout=2)
        if response.status == 200:
            break
    except OSError:
        time.sleep(0.5)
else:
    raise RuntimeError("Updated web service did not become healthy; previous binary is retained")
assert hashlib.sha256(config.read_bytes()).hexdigest() == before, "Existing configuration changed unexpectedly"
for unit in ["wechat-bot-web.service", "wechat-bot-native.service"]:
    subprocess.run(["systemctl", "--user", "is-active", "--quiet", unit], check=True)
print("Updated local web on 127.0.0.1:8081; web/native healthy, existing config unchanged, native session not restarted. No Docker export.")

# Retain a bounded rollback set only AFTER the new executable is healthy.
# This helper checks canonical paths, ELF headers and full name/hash bindings;
# no configurations, conversations, models or generated media are candidates.
sys.path.insert(0, str(workspace / "scripts"))
from binary_retention import plan_retention, apply_retention
try:
    plan = plan_retention(binary.parent, keep=3, protect_names=[backup.name])
    result = apply_retention(plan, plan["plan_hash"])
    print(f"Verified executable retention: removed {len(result['removed'])} obsolete rollback files; reclaimed {result['reclaimed_bytes']} bytes; current and recent rollback executables retained.")
except (OSError, ValueError) as exc:
    print(f"Executable retention deferred: {type(exc).__name__}; healthy service and old files preserved.")
