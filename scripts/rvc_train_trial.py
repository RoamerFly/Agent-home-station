"""Forward the historical RVC CLI to an explicitly bound installed music package.

WECHAT_MUSIC_PACKAGE_DIR + WECHAT_MUSIC_PACKAGE_SHA256 override the adjacent
rvc-training-plugin.json binding; an incomplete override fails closed.
There is no repository-source search or fallback. Training arguments, interpreter,
checkpoints and exit status belong to the selected package implementation.
"""
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import runpy
import sys


ENTRY = "runtime/training/rvc_train_trial.py"
REQUIREMENTS = "runtime/training/requirements.txt"
BINDING = "rvc-training-plugin.json"


def _unlinked(path):
    path = Path(path).absolute()
    for part in (path, *path.parents):
        if part.is_symlink() or getattr(part, "is_junction", lambda: False)():
            raise ValueError("music_training_package_link_forbidden")
    return path


def _digest(path):
    result = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1 << 20), b""):
            result.update(block)
    return result.hexdigest()


def training_resources(directory=None, manifest_sha256=None):
    """Verify an explicit deployed snapshot without importing its training code."""
    if directory is None:
        directory = os.environ.get("WECHAT_MUSIC_PACKAGE_DIR")
        override_sha256 = os.environ.get("WECHAT_MUSIC_PACKAGE_SHA256")
        if directory is not None or override_sha256 is not None:
            manifest_sha256 = override_sha256
            if not directory or not manifest_sha256:
                raise ValueError("music_training_package_binding_invalid")
        else:
            binding = _unlinked(Path(__file__).absolute().with_name(BINDING))
            if not binding.is_file():
                raise ValueError("installed_music_training_package_binding_required")
            value = json.loads(binding.read_text(encoding="utf8"))
            if not isinstance(value, dict) or set(value) != {"version", "package_directory", "manifest_sha256"} or value["version"] != 1:
                raise ValueError("music_training_package_binding_invalid")
            directory, manifest_sha256 = value["package_directory"], value["manifest_sha256"]
    if not isinstance(directory, (str, Path)) or not Path(directory).is_absolute():
        raise ValueError("installed_music_training_package_binding_required")
    directory = _unlinked(directory)
    manifest = _unlinked(directory / "plugin.json")
    actual_digest = _digest(manifest)
    if manifest_sha256 is not None and (not isinstance(manifest_sha256, str) or not re.fullmatch("[a-f0-9]{64}", manifest_sha256) or manifest_sha256 != actual_digest):
        raise ValueError("music_training_package_manifest_changed")
    value = json.loads(manifest.read_text(encoding="utf8"))
    if not isinstance(value, dict):
        raise ValueError("installed_music_training_package_required")
    files = value.get("files")
    required = {ENTRY, REQUIREMENTS, "runtime/main.py", "capabilities.json", "config.schema.json", "ui.schema.json", "README.md", "PERMISSIONS.md", "MIGRATION.md", "CHANGELOG.md"}
    if value.get("id") != "music" or value.get("schema_version") != 1 or value.get("host_protocol") != 1 or value.get("runtime") != {"transport": "mcp-stdio", "entrypoint": "runtime/main.py"} or not isinstance(value.get("version"), str) or not isinstance(files, dict) or not required.issubset(files):
        raise ValueError("installed_music_training_package_required")
    expected = {"plugin.json"}
    for name, checksum in files.items():
        member = PurePosixPath(name)
        if not name or "\\" in name or ":" in name or member.is_absolute() or any(p in (".", "..") for p in member.parts) or member.as_posix() != name or name == "plugin.json" or not isinstance(checksum, str) or not re.fullmatch("[a-f0-9]{64}", checksum):
            raise ValueError("music_training_package_index_invalid")
        file = _unlinked(directory.joinpath(*member.parts))
        if not file.is_file() or _digest(file) != checksum:
            raise ValueError("music_training_package_member_changed")
        expected.add(name)
    found = set()
    for current, directories, names in os.walk(directory, followlinks=False):
        for name in directories + names:
            _unlinked(Path(current) / name)
        found.update((Path(current) / name).relative_to(directory).as_posix() for name in names)
    if found != expected:
        raise ValueError("music_training_package_unindexed_files")
    return {"package_directory": str(directory), "manifest_sha256": actual_digest,
            "version": value["version"], "entry": str(directory / ENTRY),
            "requirements": str(directory / REQUIREMENTS)}


if __name__ == "__main__":
    try:
        resources = training_resources()
    except (OSError, ValueError, KeyError, TypeError):
        print("installed_music_training_package_missing_or_changed", file=sys.stderr)
        raise SystemExit(2)
    sys.dont_write_bytecode = True
    runpy.run_path(resources["entry"], run_name="__main__")
