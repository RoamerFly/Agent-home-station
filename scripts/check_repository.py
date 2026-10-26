"""Check the Git index without printing credentials or file contents.

Optional --private-root compares staged files with local active credential values.
It reads private files only for comparison; none of those values are emitted.
"""
import argparse
import fnmatch
import io
import json
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
DENIED_DIRS = {
    "secrets", "temp", "tmp", "dist", ".agents", ".codex", ".playwright-cli",
    "node_modules", "__pycache__", ".venv", "venv", "media-assets", "bot-state", "archive", "send-ledger",
}
DENIED_SUFFIXES = {".db", ".sqlite", ".sqlite3", ".pem", ".key", ".p12", ".pfx", ".pyc", ".log"}
PATTERNS = [
    ("provider_key", re.compile(rb"\bsk-(?:ws-)?[A-Za-z0-9_.-]{16,}")),
    ("github_token", re.compile(rb"\bgh[pousr]_[A-Za-z0-9_]{20,}")),
    ("private_key", re.compile(rb"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----")),
    ("credential_url", re.compile(rb"https?://[A-Za-z0-9._%+-]+:[A-Za-z0-9._%+-]+@")),
]


def private_values(private_root):
    values = set()
    fields = {"key", "api_key", "apikey", "token", "yue_token", "password", "secret", "cookie"}

    def collect(value):
        if isinstance(value, dict):
            for key, item in value.items():
                if key.lower() in fields and isinstance(item, str) and len(item.strip()) >= 16:
                    values.add(item.strip().encode())
                elif key.lower() == "secrets" and isinstance(item, dict):
                    values.update(v.strip().encode() for v in item.values() if isinstance(v, str) and len(v.strip()) >= 16)
                else:
                    collect(item)
        elif isinstance(value, list):
            for item in value:
                collect(item)

    for path in [ROOT / "key阿里云百炼.txt", *list((ROOT / "secrets").glob("*token*.txt")), *list((ROOT / "secrets").glob("*cookie*.txt")), ROOT / "secrets/deepseek-api-key.txt"]:
        if path.is_file():
            value = path.read_text(encoding="utf-8-sig").strip()
            if len(value) >= 16:
                values.add(value.encode())
    if private_root:
        base = Path(private_root)
        for name in ["config.json", "speech-config.json", "plugins.json", "mcp-config.json", "channels-config.json"]:
            path = base / "admin" / name
            if path.is_file():
                collect(json.loads(path.read_text(encoding="utf-8")))
        for name in ["admin/admin-token", "admin/admin-password", "api-token", "send-api-token", "admin/provider-key"]:
            path = base / name
            if path.is_file():
                value = path.read_text(encoding="utf-8").strip()
                if len(value) >= 16:
                    values.add(value.encode())
    return values


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--private-root", default="")
    args = parser.parse_args()
    result = subprocess.run(["git", "ls-files", "--stage", "-z"], cwd=ROOT, stdout=subprocess.PIPE, check=True)
    entries = []
    for item in result.stdout.split(b"\0"):
        if item:
            metadata, name = item.split(b"\t", 1)
            mode, oid, stage = metadata.decode().split()
            entries.append((mode, oid, stage, name.decode("utf-8")))
    oids = list(dict.fromkeys(oid for mode, oid, stage, path in entries if mode != "160000" and stage == "0"))
    objects = {}
    if oids:
        stream = io.BytesIO(subprocess.check_output(["git", "cat-file", "--batch"], cwd=ROOT, input=("\n".join(oids)+"\n").encode()))
        for oid in oids:
            header = stream.readline().split()
            if len(header) != 3 or header[1] != b"blob":
                raise RuntimeError("Invalid indexed Git object")
            objects[oid] = stream.read(int(header[2]))
            if stream.read(1) != b"\n":
                raise RuntimeError("Invalid Git object boundary")
    secrets = private_values(args.private_root)
    failures = []
    count = total = 0
    for mode, oid, stage, path in entries:
        parts = path.split("/")
        bad = any(p in DENIED_DIRS for p in parts)
        bad |= path.startswith(("research/evidence/", "research/samples/", "design/ui-refresh-20261024/before/"))
        bad |= parts[-1] != ".env.example" and any(fnmatch.fnmatch(parts[-1], p) for p in [".env", ".env.*", "key*.txt", "*.session", "*.session.json"])
        bad |= Path(path).suffix.lower() in DENIED_SUFFIXES
        bad |= parts[-1] in {"admin-token", "admin-password", "provider-key", "api-token", "send-api-token"}
        if bad:
            failures.append({"path": path, "reason": "private_or_generated_path"})
        if stage != "0" or mode == "160000":
            failures.append({"path": path, "reason": "unmerged_or_embedded_repository"})
            continue
        data = objects[oid]
        count += 1
        total += len(data)
        if len(data) > 50 << 20:
            failures.append({"path": path, "reason": "oversized_asset"})
        for label, pattern in PATTERNS:
            matches = list(pattern.finditer(data))
            if label == "credential_url":
                matches = [m for m in matches if m.group() not in {b"https://user:pw@", b"https://u:p@", b"https://user:pass@", b"https://user:password@", b"https://user:key@", b"https://user:secret@", b"http://user:pass@"}]
            if matches:
                failures.append({"path": path, "reason": label})
        if any(value in data for value in secrets):
            failures.append({"path": path, "reason": "matches_active_private_credential"})
    print(json.dumps({"tracked_files": count, "tracked_bytes": total, "private_values_checked": len(secrets), "findings": failures}, ensure_ascii=False, indent=2))
    return bool(failures)


if __name__ == "__main__":
    sys.exit(main())
