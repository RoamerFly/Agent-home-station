"""Import only YuE application source and API workflows, never its runtime/data.

The existing external checkout is read-only. Run explicitly after reviewing its
changes; the imported snapshot and manifest become reviewable Git changes here.
"""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess

ROOT = Path(__file__).resolve().parents[1]
WORKFLOWS = ["audio_yue2_text_to_music.json", "audio_yue2_music_cover.json", "audio_yue2_pure_music.json"]


def git(source, *args):
    return subprocess.check_output(["git", "-C", str(source), *args]).decode("utf-8").strip()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", type=Path, required=True, help="existing YuE app source directory")
    args = parser.parse_args()
    source = args.source.resolve()
    target = ROOT / "components/yue2/app"
    selected = []
    for name in git(source, "ls-files", "-z").split("\0"):
        path = Path(name)
        if name and path.suffix in {".py", ".css", ".md", ".txt", ".sh", ".bat", ".conf", ".json"} and path.parts[0] not in {"cache", "models", "outputs", "comfy"}:
            selected.append(name)
    selected += ["workflows_api/" + name for name in WORKFLOWS]
    manifest = {"component": "yue2-application", "source_url": git(source, "remote", "get-url", "origin"), "base_commit": git(source, "rev-parse", "HEAD"), "local_changes_preserved": True, "license_file_present": any(source.glob("*LICENSE*")), "files": []}
    for name in sorted(set(selected)):
        path = (source / name).resolve()
        if not path.is_relative_to(source) or not path.is_file() or path.stat().st_size > 2 << 20:
            raise SystemExit("Invalid source file: " + name)
        original = path.read_bytes()
        data = original.replace(b"\r\n", b"\n")
        destination = target / name
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_bytes(data)
        manifest["files"].append({"path": name, "sha256": hashlib.sha256(data).hexdigest(), "source_sha256": hashlib.sha256(original).hexdigest(), "bytes": len(data)})
    comfy = source / "comfy/ComfyUI"
    manifest["runtime_dependency"] = {"name": "ComfyUI", "source_url": git(comfy, "remote", "get-url", "origin"), "commit": git(comfy, "rev-parse", "HEAD"), "license": "GPL-3.0", "runtime_and_models_included": False}
    (target.parent / "source-manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"imported_files": len(manifest["files"]), "bytes": sum(f["bytes"] for f in manifest["files"]), "source_modified": False, "destination": str(target)}, ensure_ascii=False))


if __name__ == "__main__":
    main()
