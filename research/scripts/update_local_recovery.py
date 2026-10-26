"""Install source-built native/web candidates after a graceful local drain.
Preserves credentials, configuration and account data; never builds an image.
Run as the existing service user inside Ubuntu-New.
"""
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import tempfile
import time
import urllib.parse
import urllib.request

workspace = Path(__file__).resolve().parents[2]
root = Path.home() / ".local/share/wechat-bot-native-research-20261024"
assert root.stat().st_uid == os.getuid() and not root.stat().st_mode & 0o077
candidates = {"wx": workspace / "temp/wx-autonomous-recovery", "wechat-admin": workspace / "dist/linux-amd64/wechat-admin"}
assert all(p.read_bytes()[:4] == b"\x7fELF" for p in candidates.values())
protected = [root / "admin/config.json", root / "storage-key", root / "api-token"]
digest = lambda p: hashlib.sha256(p.read_bytes()).hexdigest()
before = [digest(p) for p in protected]
base = "http://127.0.0.1:8081"
client = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

def call(path, body=None, csrf=""):
    headers = {"Origin": base, "Content-Type": "application/json"}
    if csrf:
        headers["X-CSRF-Token"] = csrf
    req = urllib.request.Request(base + path, data=None if body is None else json.dumps(body).encode(), headers=headers)
    with client.open(req, timeout=12) as response:
        return json.load(response)

password = root / "admin/admin-password"
if not password.exists():
    password = root / "admin/admin-token"
csrf = call("/api/login", {"password": password.read_text().strip()})["csrf"]
ticket = call("/api/runtime/drain", {}, csrf)["ticket"]
stopped = False
backups = {}
try:
    for _ in range(20):
        if call("/api/runtime/drain?ticket=" + urllib.parse.quote(ticket))["ready"]:
            break
        time.sleep(1)
    else:
        raise RuntimeError("Busy worker; update deferred without cancellation")
    db = sqlite3.connect("file:" + str(root / "accounts/background-test-20261024/agent/control.sqlite") + "?mode=ro", uri=True)
    assert db.execute("select count(*) from media_jobs where state in ('queued','running')").fetchone()[0] == 0
    assert before == [digest(p) for p in protected]
    for name in candidates:
        binary = root / "bin" / name
        backup = binary.with_name(name + ".previous-" + digest(binary)[:12])
        if not backup.exists():
            shutil.copyfile(binary, backup)
            backup.chmod(0o700)
        backups[name] = backup
    subprocess.run(["systemctl", "--user", "stop", "wechat-bot-web.service"], check=True)
    stopped = True
    try:
        assert db.execute("select count(*) from media_jobs where state in ('queued','running')").fetchone()[0] == 0
        subprocess.run(["systemctl", "--user", "stop", "wechat-bot-native.service"], check=True)
        for name, source in candidates.items():
            binary = root / "bin" / name
            with tempfile.NamedTemporaryFile(dir=binary.parent, delete=False) as f:
                pending = Path(f.name)
                os.fchmod(f.fileno(), 0o700)
                f.write(source.read_bytes())
                f.flush()
                os.fsync(f.fileno())
            pending.replace(binary)
    finally:
        subprocess.run(["systemctl", "--user", "start", "wechat-bot-native.service"], check=True)
        subprocess.run(["systemctl", "--user", "start", "wechat-bot-web.service"], check=True)
    assert before == [digest(p) for p in protected]
    for _ in range(40):
        try:
            csrf = call("/api/login", {"password": password.read_text().strip()})["csrf"]
            status = call("/api/status")
            if status["native"]["authenticated"] and status["native"]["connection"] == "online" and status["bot"]["state"] == "running":
                break
        except (OSError, KeyError):
            pass
        time.sleep(1)
    else:
        raise RuntimeError("Services still reconnecting; credentials and binary backups preserved")
    report = {"version": status["version"], "native_authenticated": True, "native_online": True, "bot_running": True, "config_and_credentials_unchanged": True, "binary_hashes": {name: digest(root / "bin" / name) for name in candidates}, "docker_images_built_or_exported": 0}
    (workspace / "research/evidence/autonomous-recovery-local-update.json").write_text(json.dumps(report, indent=2), encoding="utf8")
    print(json.dumps(report))
finally:
    if not stopped:
        try:
            call("/api/runtime/undrain", {"ticket": ticket}, csrf)
        except OSError:
            pass
