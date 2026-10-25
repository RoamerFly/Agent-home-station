"""Capture the reviewed native-client changes without copying runtime data."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", type=Path, required=True)
    args = parser.parse_args()
    source = args.source.resolve()
    base = subprocess.check_output(["git", "-C", str(source), "rev-parse", "HEAD"]).decode().strip()
    remote = subprocess.check_output(["git", "-C", str(source), "remote", "get-url", "origin"]).decode().strip()
    patch = subprocess.check_output(["git", "-C", str(source), "diff", "--binary", "HEAD", "--", "packages/native-client-rust"])
    new_files = subprocess.check_output(["git", "-C", str(source), "ls-files", "--others", "--exclude-standard", "-z", "--", "packages/native-client-rust"]).decode().split("\0")
    for name in new_files:
        if not name:
            continue
        if Path(name).suffix not in {".rs", ".toml", ".md", ".json", ".sh"}:
            raise SystemExit("Review new native file before importing: " + name)
        result = subprocess.run(["git", "diff", "--no-index", "--binary", "--", "/dev/null", name], cwd=source, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        if result.returncode != 1:
            raise SystemExit("Cannot capture new source file: " + name)
        patch += result.stdout
    if not patch:
        raise SystemExit("No native-client changes to capture")
    patch = patch.replace(b"\r\n", b"\n")
    destination = ROOT / "research/patches/native-client-local-integration.patch"
    destination.write_bytes(patch)
    manifest = {"component": "agent-wechat-native", "source_url": remote, "base_commit": base, "patch": "research/patches/" + destination.name, "patch_sha256": hashlib.sha256(patch).hexdigest(), "new_source_files": [n for n in new_files if n], "source_tree_has_local_changes": True, "capture_modified_source": False, "account_data_included": False}
    (ROOT / "components/native-client").mkdir(parents=True, exist_ok=True)
    (ROOT / "components/native-client/source-manifest.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"base_commit": base, "patch_bytes": len(patch), "capture_modified_source": False}))


if __name__ == "__main__":
    main()
