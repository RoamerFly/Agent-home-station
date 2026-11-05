"""Migrate only the obsolete built-in Docker note when using local WSL.

Project paths, caller-authored instructions and model/runtime configuration
remain unchanged. This changes documentation, never starts music or inference.
"""
import json
import os
from pathlib import Path
import tempfile

DOCKER_NOTE = (
    "Docker music worker; invoke music_local_start/status or rvc_cover_start/status. "
    "Linux GPU runtime is in the optional music image; core chat image does not contain Torch."
)
LOCAL_NOTE = (
    "Local WSL/Linux music service with Bubblewrap isolation. Use the registered "
    "managed interpreter and project README, or music_local_start/status and "
    "music_rvc_start with music_local_status. No Docker image is required for this local setup. "
    "Check service health, GPU availability and actual task results separately."
)


def refresh_execution_notes(workspace, backend):
    if backend != "wsl":
        return 0
    directory = Path(workspace) / "public_workspace" / "manifests"
    for parent in [directory, directory.parent, Path(workspace)]:
        if parent.is_symlink() or getattr(parent, "is_junction", lambda: False)():
            raise ValueError("project_guide_directory_is_link")
    updated = 0
    for name in ("rvc.json", "yue.json"):
        path = directory / name
        if path.is_symlink() or getattr(path, "is_junction", lambda: False)():
            raise ValueError("project_guide_is_link")
        if not path.is_file():
            continue
        original = path.read_bytes()
        data = json.loads(original.decode("utf-8-sig"))
        if data.get("execution") != DOCKER_NOTE:
            continue
        data["execution"] = LOCAL_NOTE
        pending = None
        try:
            with tempfile.NamedTemporaryFile(dir=directory, delete=False) as stream:
                pending = Path(stream.name)
                stream.write((json.dumps(data, ensure_ascii=False, indent=2) + "\n").encode())
                stream.flush()
                os.fsync(stream.fileno())
            if path.is_symlink() or path.read_bytes() != original:
                raise ValueError("project_guide_changed_during_update")
            pending.replace(path)
            updated += 1
        finally:
            if pending is not None:
                pending.unlink(missing_ok=True)
    return updated
