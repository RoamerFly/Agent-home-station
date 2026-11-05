"""Rebase a stopped native data copy inside a NEW Docker volume.

Never run against the active source directory. Secrets and message text are
not printed or rewritten. Only registry paths and known control-path fields
are rebased; old evidence/snapshots remain byte-for-byte intact.
"""
import argparse
import json
import os
from pathlib import Path
import sqlite3

PATH_FIELDS = {"path", "local_path", "file_path", "source_path", "output_path",
               "state_dir", "data_dir", "profile_path", "token_file", "binary",
               "python", "helper", "spool", "shell", "roots"}
CONTROL_FILES = {"agent-config.json", "config.json", "plugins.json", "mcp-config.json",
                 "speech-config.json", "channels-config.json", "media-assets.json",
                 "media-recovery.json", "tool-state.json"}


def rebase_registry(db, old):
    # The profile key manifest is a separate registry path. Moving only the
    # profile directory would preserve bytes but break saved-login recovery.
    for table, identity, column in (("accounts", "id", "profile_path"),
                                    ("account_settings", "account_id", "keys_file")):
        for account, path in db.execute(f"SELECT {identity}, {column} FROM {table}").fetchall():
            if path is None:
                continue
            if not path.startswith(old + "/"):
                raise ValueError("profile_or_keys_outside_migrated_native_root")
            db.execute(f"UPDATE {table} SET {column}=? WHERE {identity}=?", ("/data" + path[len(old):], account))


def rebase(value, old, key=""):
    if isinstance(value, dict):
        return {name: rebase(item, old, name) for name, item in value.items()}
    if isinstance(value, list):
        return [rebase(item, old, key) for item in value]
    if isinstance(value, str) and key in PATH_FIELDS and (value == old or value.startswith(old + "/")):
        return "/data" + value[len(old):]
    return value


def prepare(root, old, pause=False):
    root = Path(root).resolve(strict=True)
    if str(root) != "/data" or not old.startswith("/home/") or ".." in Path(old).parts:
        raise ValueError("expected_new_docker_data_volume_and_old_home_root")
    marker = root / "docker-migration.json"
    if marker.exists():
        raise ValueError("volume_already_prepared")
    changed = 0
    resume = []
    registry = root / "registry.sqlite"
    if registry.exists():
        with sqlite3.connect(registry) as db:
            rebase_registry(db, old)
    # All account manager directories use the same small set of control names.
    # Do not recursively modify exports, notes, task requests or media files.
    admins = [root / "admin"]
    accounts = root / "admin" / "accounts"
    if accounts.is_dir():
        admins += [p for p in accounts.iterdir() if p.is_dir() and not p.is_symlink()]
    for admin in admins:
        for name in CONTROL_FILES:
            file = admin / name
            if not file.is_file():
                continue
            before = json.loads(file.read_bytes())
            after = rebase(before, old)
            if name == "agent-config.json":
                host = after.setdefault("host", {})
                host.update(mode="full", roots=["/workspace"], windows=None)
            if pause and name == "config.json" and after.get("enabled"):
                after["enabled"] = False
                resume.append(str(admin.relative_to(root)))
            if before == after:
                continue
            temp = file.with_suffix(".docker-tmp")
            with temp.open("w", encoding="utf-8") as stream:
                json.dump(after, stream, ensure_ascii=False, indent=2)
            temp.chmod(0o600)
            os.replace(temp, file)
            changed += 1
    marker.write_text(json.dumps({"format": 1, "source_root": old, "data_root": "/data",
                                 "workspace_root": "/workspace", "control_files_rebased": changed,
                                 "resume_managers": resume}), encoding="utf-8")
    marker.chmod(0o600)
    return {"prepared": True, "control_files_rebased": changed}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--old-root", required=True)
    parser.add_argument("--data", default="/data")
    parser.add_argument("--pause", action="store_true", help="Hold automatic replies until migration checks finish")
    args = parser.parse_args()
    print(json.dumps(prepare(args.data, args.old_root, args.pause)))
