"""Install the checksum-pinned public GPT-SoVITS v2 enrollment base weights.

The lock is deliberately separate from a user's voice library.  It refuses a
private checkpoint or an unverified download, and does not alter any active
voice selection.  Example:

  python provision_enrollment_v2.py --base-models <public models/gpt-sovits>
"""
import argparse
import hashlib
import json
from pathlib import Path
import tempfile
import urllib.request
import os
import time


def digest(path):
    h = hashlib.sha256()
    with path.open("rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def install(root, item):
    target = (root / item["relative_path"]).resolve()
    if not target.is_relative_to(root.resolve()):
        raise ValueError("unsafe_locked_relative_path")
    target.parent.mkdir(parents=True, exist_ok=True)
    if target.exists():
        if target.is_symlink() or target.stat().st_size != item["bytes"] or digest(target) != item["sha256"]:
            raise ValueError("existing_base_model_hash_mismatch:" + item["relative_path"])
        return {"key": item["key"], "state": "already_verified", "path": str(target)}
    with tempfile.NamedTemporaryFile(dir=target.parent, prefix=".download-", delete=False) as out:
        pending = Path(out.name)
    try:
        # urllib honors normal HTTP(S)_PROXY settings. Credentials never enter
        # this lock or command line. Retry transient connections a bounded
        # number of times and never trust a partial blob.
        last = None
        for attempt in range(3):
            pending.unlink(missing_ok=True)
            try:
                request = urllib.request.Request(item["url"], headers={"User-Agent": "wechat-agent-base-provisioner/1"})
                with urllib.request.urlopen(request, timeout=90) as incoming, pending.open("wb") as out:
                    while True:
                        block = incoming.read(1024 * 1024)
                        if not block:
                            break
                        out.write(block)
                if pending.stat().st_size == item["bytes"] and digest(pending) == item["sha256"]:
                    break
                last = ValueError("download_hash_mismatch:" + item["key"])
            except (OSError, ValueError) as error:
                last = error
            if attempt < 2:
                time.sleep(1 << attempt)
        else:
            raise ValueError("download_failed_after_retries:" + item["key"] + ":" + str(last))
        if pending.stat().st_size != item["bytes"] or digest(pending) != item["sha256"]:
            raise ValueError("download_hash_mismatch:" + item["key"])
        try:
            os.link(pending, target)
        except FileExistsError:
            if target.stat().st_size != item["bytes"] or digest(target) != item["sha256"]:
                raise ValueError("concurrent_base_model_mismatch:" + item["key"])
        return {"key": item["key"], "state": "installed", "path": str(target)}
    finally:
        pending.unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-models", required=True, type=Path)
    parser.add_argument("--lock", type=Path, default=Path(__file__).parents[1] / "base_models" / "enrollment-v2.json")
    args = parser.parse_args()
    root = args.base_models.resolve()
    root.mkdir(parents=True, exist_ok=True)
    if root.is_symlink():
        raise ValueError("base_models_link_not_allowed")
    lock = json.loads(args.lock.read_text(encoding="utf8"))
    if lock.get("schema_version") != 1 or lock.get("engine_version") != "v2":
        raise ValueError("unsupported_base_model_lock")
    entries = []
    for key in ("gpt", "sovits", "discriminator"):
        item = lock.get(key)
        if not isinstance(item, dict):
            raise ValueError("base_model_lock_missing_" + key)
        entries.append(dict(item, key=key))
    print(json.dumps({"installed": [install(root, item) for item in entries], "lock": str(args.lock.resolve())}, ensure_ascii=False))


if __name__ == "__main__":
    main()
