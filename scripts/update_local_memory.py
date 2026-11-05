"""Drain, back up, migrate and activate the local Web memory upgrade.

Run in the existing Linux deployment as its owner. Native WeChat stays running;
the script performs authenticated read-only checks and sends no test message.
Backups are private and stay outside Git. Before activation an old program can
be restored; after activation failures preserve SQLite and never roll back JSON.
"""
from pathlib import Path
import argparse
from contextlib import closing
import hashlib
import http.cookiejar
import json
import os
import shutil
import sqlite3
import subprocess
import tempfile
import time
import urllib.parse
import urllib.request


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def metadata_backup(source, target):
    """Back up mutable control data; immutable media/models stay in place."""
    skipped = {"media-assets", "manual-uploads", "validation-media", "incoming-transfers",
               "tool-outputs", "music-audio-reviews", "repair-test-runs"}
    for folder, dirs, files in os.walk(source, followlinks=False):
        folder = Path(folder)
        dirs[:] = [n for n in dirs if n not in skipped and not (folder / n).is_symlink()]
        dest = target / folder.relative_to(source)
        dest.mkdir(parents=True, exist_ok=True, mode=0o700)
        for name in files:
            item = folder / name
            if item.is_symlink() or name.endswith(("-wal", "-shm", ".lock")):
                continue
            out = dest / name
            with item.open("rb") as stream:
                header = stream.read(16)
            if header == b"SQLite format 3\x00":
                with closing(sqlite3.connect("file:" + str(item) + "?mode=ro", uri=True)) as src:
                    with closing(sqlite3.connect(out)) as dst:
                        src.backup(dst)
                        dst.execute("PRAGMA wal_checkpoint(TRUNCATE)")
                        dst.execute("PRAGMA journal_mode=DELETE")
            else:
                shutil.copyfile(item, out)
            out.chmod(0o600)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--expected-sha", required=True, help="Reviewed, pushed and CI-verified source SHA")
    parser.add_argument("--drain-timeout-seconds", type=float, default=0, help="0 waits until current work finishes")
    args = parser.parse_args()
    workspace = Path(__file__).resolve().parents[1]
    head = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=workspace, text=True).strip()
    if head != args.expected_sha:
        raise RuntimeError("Source SHA changed since validation")
    deployment = json.loads((workspace / "secrets/local-deployment.json").read_text(encoding="utf-8-sig"))
    root = Path(deployment["data_root"]).resolve(strict=True)
    if root.stat().st_uid != os.getuid() or root.stat().st_mode & 0o077:
        raise RuntimeError("Deployment root must be private and owned by this user")
    admin = root / "admin"
    binary = root / "bin/wechat-admin"
    source = workspace / "dist/linux-amd64/wechat-admin"
    migrator = workspace / "dist/linux-amd64/wechat-memory"
    for p in (source, migrator):
        if p.read_bytes()[:4] != b"\x7fELF":
            raise RuntimeError("Expected validated Linux executables")
    base = "http://127.0.0.1:8081"
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}),
        urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def request(route, body=None, csrf=""):
        headers = {"Origin": base, "Content-Type": "application/json"}
        if csrf:
            headers["X-CSRF-Token"] = csrf
        req = urllib.request.Request(base + route, headers=headers,
            data=None if body is None else json.dumps(body).encode())
        with client.open(req, timeout=20) as response:
            return json.load(response)

    password = admin / "admin-password"
    if not password.exists():
        password = admin / "admin-token"

    def login():
        return request("/api/login", {"password": password.read_text().strip()})["csrf"]

    hold = admin / "memory-update-hold"
    if hold.exists():
        raise RuntimeError("An earlier memory migration is held; inspect its backup before retrying")
    csrf = login()
    native_before = request("/api/status")["native"]
    ticket = request("/api/runtime/drain", {}, csrf)["ticket"]
    backup = root / "backups" / ("memory-upgrade-" + time.strftime("%Y%m%d-%H%M%S"))
    stopped = activated = backup_complete = False
    try:
        started = time.monotonic()
        while not request("/api/runtime/drain?ticket=" + urllib.parse.quote(ticket))["ready"]:
            if args.drain_timeout_seconds and time.monotonic() - started >= args.drain_timeout_seconds:
                raise RuntimeError("Current work is still running; update deferred without cancellation")
            if int(time.monotonic() - started) % 30 == 0:
                print("Waiting for in-flight work; no cancellation or real test sends.", flush=True)
            time.sleep(1)
        backup.mkdir(parents=True, mode=0o700)
        hold.write_text(json.dumps({"sha": head, "backup": str(backup)}))
        hold.chmod(0o600)
        subprocess.run(["systemctl", "--user", "stop", "wechat-bot-web.service"], check=True)
        stopped = True
        shutil.copyfile(binary, backup / "wechat-admin")
        (backup / "wechat-admin").chmod(0o700)
        metadata_backup(admin, backup / "admin")
        metadata_backup(root / "bot-state", backup / "bot-state")
        backup_complete = True
        controls = [p for p in (admin / "config.json", admin / "media-assets.json", root / "bot-state/messages-v2.json") if p.exists()]
        hashes = {str(p): digest(p) for p in controls}
        companions = [admin / "companion", *sorted(admin.glob("accounts/*/companion"))]
        report = {"sha": head, "backup": str(backup), "migrations": [], "activated": False,
                  "native_restarted": False, "real_wechat_test_sends": 0}
        for folder in companions:
            if not folder.exists():
                continue
            runs = {}
            for action in ("inspect", "dry-run", "migrate", "verify"):
                result = subprocess.check_output([str(migrator), "--dir", str(folder), "--action", action], text=True)
                runs[action] = json.loads(result)
            while True:
                result = subprocess.check_output([str(migrator), "--dir", str(folder), "--action", "index",
                                                  "--all-scopes", "--batches", "20"], text=True)
                runs["index"] = json.loads(result)
                if runs["index"]["complete"]:
                    break
                print("Archive indexing checkpoint committed; continuing with Web held.", flush=True)
            runs["verify_after_index"] = json.loads(subprocess.check_output(
                [str(migrator), "--dir", str(folder), "--action", "verify"], text=True))
            report["migrations"].append({"folder": str(folder.relative_to(root)), "checks": runs})
        with tempfile.NamedTemporaryFile(dir=binary.parent, delete=False) as stream:
            pending = Path(stream.name)
            os.fchmod(stream.fileno(), 0o700)
            stream.write(source.read_bytes())
            stream.flush()
            os.fsync(stream.fileno())
        pending.replace(binary)
        subprocess.run(["systemctl", "--user", "start", "wechat-bot-web.service"], check=True)
        deadline = time.monotonic() + 60
        while True:
            try:
                csrf = login()
                with client.open(base + "/healthz", timeout=2) as response:
                    if response.status == 200:
                        break
            except OSError:
                pass
            if time.monotonic() > deadline:
                raise RuntimeError("Updated Web did not become healthy; activation remains blocked")
            time.sleep(.5)
        # Native/login readiness is observed, never reauthenticated or restarted.
        for unit in ("wechat-bot-web.service", "wechat-bot-native.service"):
            subprocess.run(["systemctl", "--user", "is-active", "--quiet", unit], check=True)
        report["held_memory_status"] = request("/api/memory/status")
        runtime = request("/api/status")
        if native_before.get("authenticated") and not runtime["native"].get("authenticated"):
            raise RuntimeError("Native login became unavailable; activation remains blocked")
        report["native_authenticated"] = runtime["native"].get("authenticated", False)
        report["native_connection"] = runtime["native"].get("connection", "unknown")
        # Read an authorized current-account original through the protected API.
        # Only aggregate verification is recorded; private bodies/identities are
        # neither printed nor copied into the source repository.
        verified_reads = 0
        with closing(sqlite3.connect("file:" + str(admin / "companion/memory.sqlite") + "?mode=ro", uri=True)) as db:
            candidates = db.execute("SELECT e.scope,e.id FROM events e JOIN scopes s ON s.id=e.scope "
                "WHERE json_extract(s.payload,'$.account')=? AND e.suppressed=0 "
                "AND NOT EXISTS(SELECT 1 FROM memory_blocks b WHERE b.scope=e.scope AND b.event_id=e.id) "
                "ORDER BY e.seq DESC LIMIT 1", (runtime["account"],)).fetchall()
        for scope_id, event_id in candidates:
            prefix = "/api/memory/" + urllib.parse.quote(scope_id, safe="")
            page = request(prefix + "?originals=true&limit=1")
            event = request(prefix + "/sources/" + urllib.parse.quote(event_id, safe=""))
            if event.get("id") != event_id or not isinstance(page.get("page", {}).get("items"), list):
                raise RuntimeError("Authenticated original read verification failed")
            verified_reads += 1
        report["authenticated_source_reads"] = verified_reads
        if any(digest(Path(p)) != h for p, h in hashes.items()):
            raise RuntimeError("Authoritative config/media/inbox changed before activation; inspect backup")
        report["configuration_media_inbox_unchanged"] = True
        # This is the irreversible data-version boundary. Never auto-restore
        # JSON if activation succeeded or its HTTP outcome is uncertain.
        activated = True
        result = request("/api/runtime/memory-activate", {}, csrf)
        report["activated"] = result["activated"]
        report["program_sha256"] = digest(binary)
        (backup / "result.json").write_text(json.dumps(report, ensure_ascii=False, indent=2))
        print(json.dumps({"sha": head, "backup": str(backup), "activated": True,
                          "program_sha256": report["program_sha256"], "real_wechat_test_sends": 0}))
    except BaseException:
        if stopped and not activated and not backup_complete:
            # No migration/program replacement has started yet.
            hold.unlink(missing_ok=True)
            subprocess.run(["systemctl", "--user", "start", "wechat-bot-web.service"], check=True)
        elif stopped and not activated and (backup / "wechat-admin").exists():
            subprocess.run(["systemctl", "--user", "stop", "wechat-bot-web.service"], check=True)
            # Preserve new SQLite for diagnosis; old JSON was never overwritten.
            for folder in [admin / "companion", *admin.glob("accounts/*/companion")]:
                prior = backup / "admin" / folder.relative_to(admin) / "memory.sqlite"
                if (folder / "memory.sqlite").exists():
                    evidence = backup / "failed-memory" / folder.relative_to(admin)
                    evidence.mkdir(parents=True, exist_ok=True, mode=0o700)
                    for name in ("memory.sqlite", "memory.sqlite-wal", "memory.sqlite-shm"):
                        p = folder / name
                        if p.exists():
                            p.replace(evidence / name)
                if prior.exists():
                    shutil.copyfile(prior, folder / "memory.sqlite")
                    (folder / "memory.sqlite").chmod(0o600)
            shutil.copyfile(backup / "wechat-admin", binary)
            binary.chmod(0o700)
            hold.unlink(missing_ok=True)
            subprocess.run(["systemctl", "--user", "start", "wechat-bot-web.service"], check=True)
        elif not stopped:
            hold.unlink(missing_ok=True)
            request("/api/runtime/undrain", {"ticket": ticket}, csrf)
        raise


if __name__ == "__main__":
    main()
