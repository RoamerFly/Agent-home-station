"""Prepare a new offline WeChat memory snapshot for the existing archive preview."""

import argparse
import base64
from collections import Counter
from contextlib import closing
from datetime import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import sqlite3
import subprocess
import sys
import uuid


def digest(path):
    with path.open("rb") as file:
        return hashlib.file_digest(file, "sha256").hexdigest()


def hash_text(text):
    return hashlib.sha256(text.encode()).hexdigest()


def immutable(path):
    return sqlite3.connect(path.as_uri() + "?mode=ro&immutable=1", uri=True)


def quiet(path):
    for suffix in ("-wal", "-journal"):
        sidecar = path.with_name(path.name + suffix)
        if sidecar.exists() and sidecar.stat().st_size:
            raise ValueError("source has pending SQLite journal records")


def protect_file(path):
    quoted = "'" + str(path).replace("'", "''") + "'"
    command = (
        "$ErrorActionPreference='Stop'; $p=" + quoted + "; "
        "$sid=[Security.Principal.WindowsIdentity]::GetCurrent().User.Value; "
        "$a=Get-Acl -LiteralPath $p; "
        "$a.SetSecurityDescriptorSddlForm(('D:P(A;;FA;;;SY)(A;;FA;;;'+$sid+')'),"
        "[Security.AccessControl.AccessControlSections]::Access); "
        "Set-Acl -LiteralPath $p -AclObject $a"
    )
    encoded = base64.b64encode(command.encode("utf-16-le")).decode()
    # Codex can run under PowerShell 7, whose module search path is not
    # compatible with the Windows PowerShell ACL cmdlets.
    environment = {key: value for key, value in os.environ.items() if key.lower() != "psmodulepath"}
    powershell = Path(os.environ["SystemRoot"]) / "System32/WindowsPowerShell/v1.0/powershell.exe"
    result = subprocess.run([str(powershell), "-NoProfile", "-NonInteractive", "-EncodedCommand", encoded],
                            capture_output=True, timeout=30, creationflags=subprocess.CREATE_NO_WINDOW, env=environment)
    if result.returncode:
        raise ValueError("cannot protect temporary native reader input")


def native_id(message):
    source = message["id"]
    fields = {key: source[key] for key in ("database", "table", "localId")}
    payload = json.dumps(fields, ensure_ascii=False, separators=(",", ":")).encode()
    return "msg_" + base64.urlsafe_b64encode(payload).decode().rstrip("=")


def source_time(value):
    if not re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.0{1,9})?Z", value):
        return None
    return int(datetime.fromisoformat(value[:19] + "+00:00").timestamp())


def read_messages(binary, config, thread, limit, timeout):
    cursor = None
    seen = set()
    while True:
        args = [str(binary), "--json", "--config", str(config), "profile", "messages",
                "--chat", thread, "--source-stopped", "--limit", str(limit)]
        if cursor:
            args.extend(["--cursor", cursor])
        result = subprocess.run(args, capture_output=True, timeout=timeout, creationflags=subprocess.CREATE_NO_WINDOW)
        if result.returncode:
            raise ValueError("offline native reader could not read the selected profile")
        page = json.loads(result.stdout)
        if not isinstance(page.get("items"), list) or not re.fullmatch(r"[0-9a-f]{64}", page.get("sourceView", "")):
            raise ValueError("offline native reader returned invalid source evidence")
        yield page
        cursor = page.get("nextCursor")
        if not cursor:
            break
        if cursor in seen:
            raise ValueError("offline native cursor failed to advance")
        seen.add(cursor)


def corroborate(db, scope, messages, inbound, recalls, instance, generation):
    thread, account, scope_id = scope["thread"], scope["account"], scope["id"]
    by_id = {}
    server_counts = Counter()
    for message in messages:
        key = native_id(message)
        if key in by_id:
            raise ValueError("native reader returned duplicate local message IDs")
        by_id[key] = message
        server_counts[message.get("serverMessageId")] += 1
    rows = db.execute("""SELECT id,trace,sender,at,text,hash,payload FROM events e
        WHERE scope=? AND role='user' AND complete=1 AND revision=1 AND suppressed=0
        AND NOT EXISTS(SELECT 1 FROM memory_blocks b WHERE b.scope=e.scope AND b.event_id=e.id)
        AND NOT EXISTS(SELECT 1 FROM source_conflicts c WHERE c.scope=e.scope AND c.event_id=e.id)
        AND (SELECT COUNT(*) FROM events other WHERE other.scope=e.scope AND other.trace=e.trace AND other.role='user')=1""", (scope_id,)).fetchall()
    count = 0
    for event, trace, sender, at, text, text_hash, payload in rows:
        turn = json.loads(payload)
        if turn.get("source") is not None or turn.get("user_text_state") not in (None, "", "verified_original"):
            continue
        if any(turn.get(k) != v for k, v in (("event_id", event), ("message_id", trace), ("role", "user"), ("sender", sender))):
            continue
        stamp = source_time(at)
        if stamp is None or source_time(turn.get("time", "")) != stamp:
            continue
        message = by_id.get(trace)
        if not message or (thread, trace) not in inbound or message.get("messageType") != 1:
            continue
        server = message.get("serverMessageId")
        if not server or server_counts[server] != 1 or (thread, server) in recalls:
            continue
        content = message.get("content") or {}
        if message.get("peer") != thread or message.get("sender") != sender or message.get("createTime") != stamp:
            continue
        if content.get("state") not in ("utf8", "zstd") or not text.strip() or content.get("text") != text or hash_text(text) != text_hash:
            continue
        turn["user_text_state"] = "verified_original"
        turn["source"] = {"instance_id": instance, "event_id": event, "message_id": trace, "server_id": server,
                          "account": account, "thread": thread, "sender": sender, "message_type": 1, "occurred_at": at,
                          "revision": 1, "body_hash": text_hash, "integrity": "verified_import", "offline_profile_generation": generation}
        db.execute("UPDATE events SET payload=? WHERE id=?", (json.dumps(turn, ensure_ascii=False).encode(), event))
        count += 1
    return count


def prepare(binary, legacy_root, account, output, limit, timeout):
    if sys.platform != "win32" or sys.version_info < (3, 13):
        raise ValueError("use the Windows candidate's Python 3.13 runtime")
    if not re.fullmatch(r"[A-Za-z0-9_-]{1,128}", account):
        raise ValueError("invalid managed account name")
    if output.exists() or output.is_relative_to(legacy_root) or not output.parent.is_dir():
        raise ValueError("select a new output outside the legacy data directory")
    managed = legacy_root / "accounts" / account
    memory = legacy_root / "admin/companion/memory.sqlite"
    control = managed / "agent/control.sqlite"
    keys = managed / "agent/profile-keys.json"
    profiles = list((managed / "profile/xwechat_files").glob("*/db_storage"))
    if len(profiles) != 1:
        raise ValueError("managed account must identify one official profile")
    profile = profiles[0].parent
    quiet(memory)
    quiet(control)
    def source_digests():
        files = [path for path in (profile / "db_storage").rglob("*") if path.is_file()]
        return {path: digest(path) for path in [*files, memory, control, keys]}
    before = source_digests()
    scratch = output.parent / (".wechat-proof-" + uuid.uuid4().hex)
    scratch.mkdir(mode=0o700)
    config, key_copy, stage = (scratch / name for name in ("client.toml", "profile-keys.json", "memory.sqlite"))
    try:
        shutil.copyfile(keys, key_copy)
        protect_file(key_copy)
        config.write_text('schemaVersion = 1\nlocalProfile = "archive"\n[localProfiles.archive]\nformat = "official-linux"\npath = '
                          + json.dumps(str(profile), ensure_ascii=False) + '\nkeysFile = '
                          + json.dumps(str(key_copy), ensure_ascii=False) + '\n', encoding="utf-8")
        protect_file(config)
        shutil.copyfile(memory, stage)
        protect_file(stage)
        with closing(immutable(control)) as ledger:
            inbound = set(ledger.execute("SELECT json_extract(data_json,'$.threadId'),json_extract(data_json,'$.messageId') FROM events WHERE kind='message.committed' AND json_extract(data_json,'$.direction')='incoming'"))
            recalls = set(ledger.execute("SELECT thread_id,server_id FROM message_recalls"))
            recalls.update(ledger.execute("SELECT json_extract(data_json,'$.threadId'),json_extract(data_json,'$.serverId') FROM events WHERE kind='message.recalled'"))
        count = messages = 0
        generation = None
        with closing(sqlite3.connect(stage)) as db:
            db.execute("PRAGMA trusted_schema=OFF")
            db.execute("PRAGMA journal_mode=DELETE")
            if db.execute("PRAGMA quick_check(1)").fetchone() != ("ok",):
                raise ValueError("legacy memory integrity check failed")
            instance_row = db.execute("SELECT value FROM meta WHERE key='source_instance'").fetchone()
            if not instance_row or not re.fullmatch(r"[0-9a-f]{48}", instance_row[0]):
                raise ValueError("legacy source instance is unavailable")
            scopes = []
            for key, payload in db.execute("SELECT id,payload FROM scopes ORDER BY id"):
                scope = json.loads(payload)
                if scope.get("channel") != "wechat" or scope.get("account") != account or scope.get("recall_generation", 0) != 0:
                    continue
                thread = scope.get("thread")
                if not thread or key != scope.get("id") or key != hash_text("wechat\0" + account + "\0" + thread):
                    raise ValueError("legacy conversation identity mismatch")
                scopes.append(scope)
            if not scopes:
                raise ValueError("no readable legacy scope for the selected managed account")
            for scope in scopes:
                items = []
                for page in read_messages(binary, config, scope["thread"], limit, timeout):
                    if generation is not None and page["sourceView"] != generation:
                        raise ValueError("native profile generation changed")
                    generation = page["sourceView"]
                    items.extend(page["items"])
                messages += len(items)
                count += corroborate(db, scope, items, inbound, recalls, instance_row[0], generation)
            db.commit()
        quiet(memory)
        quiet(control)
        if before != source_digests():
            raise ValueError("legacy source changed during offline verification")
        if count:
            os.link(stage, output)
        return {"conversations_checked": len(scopes), "native_messages_checked": messages,
                "originals_corroborated": count, "snapshot_created": bool(count), "source_unchanged": True}
    finally:
        for item in (config, key_copy, stage, stage.with_name("memory.sqlite-journal")):
            item.unlink(missing_ok=True)
        scratch.rmdir()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--native-reader", type=Path, required=True, help="wx.exe from the verified candidate runtime")
    parser.add_argument("--legacy-root", type=Path, required=True)
    parser.add_argument("--account", required=True)
    parser.add_argument("--output", type=Path, required=True, help="new snapshot file in a private directory")
    parser.add_argument("--page-size", type=int, choices=range(1, 201), default=200, metavar="1..200")
    parser.add_argument("--page-timeout", type=float, default=90)
    parser.add_argument("--source-stopped", action="store_true", required=True)
    args = parser.parse_args()
    if args.page_timeout <= 0:
        parser.error("page timeout must be positive")
    try:
        result = prepare(args.native_reader.resolve(strict=True), args.legacy_root.resolve(strict=True),
                         args.account, args.output.resolve(), args.page_size, args.page_timeout)
    except (OSError, ValueError, sqlite3.Error, subprocess.SubprocessError):
        print("Offline evidence preparation stopped. Verify the stopped managed source, reader and private destination.", file=sys.stderr)
        return 1
    print(json.dumps(result))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
