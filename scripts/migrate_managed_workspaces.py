"""Copy registered project resources into managed workspaces without deleting originals.

Plan JSON: {root, copies:[{source,target,exclude?:[],exclude_paths?:[]}]}.
exclude matches recursive basenames (legacy); exclude_paths matches relative
paths at the source root, so excluding model weights need not drop code such
as comfy/ldm/models/autoencoder.py.
Targets must be under root/public_workspace or root/pri_workspace. Existing
different files abort instead of overwriting. Each file is verified by SHA-256;
repeating an interrupted migration verifies/reuses the completed files.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import tempfile


def digest(path):
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(4 * 1024 * 1024), b""):
            value.update(chunk)
    return value.hexdigest()


def migrate(plan, apply=False):
    root = Path(plan["root"]).resolve(strict=True)
    approved = [root / "public_workspace", root / "pri_workspace"]
    if apply:
        for base in approved:
            if base.is_symlink() or getattr(base, "is_junction", lambda: False)(): raise ValueError("destination_link_not_allowed")
            base.mkdir(parents=True,exist_ok=True)
            marker=base/"go.mod"
            if not marker.exists():
                marker.write_text("module managed.local/"+base.name+"\n\ngo 1.25.0\n",encoding="utf8")
    count = size = 0
    for spec in plan["copies"]:
        source = Path(spec["source"]).resolve(strict=True)
        target = Path(spec["target"])
        if not target.is_absolute():
            target = root / target
        for parent in [target, *target.parents]:
            if parent.is_symlink() or getattr(parent, "is_junction", lambda: False)():
                raise ValueError("destination_link_not_allowed")
        target = target.resolve()
        if not any(target.is_relative_to(base) and target != base for base in approved):
            raise ValueError("target_outside_managed_workspace")
        if target.is_relative_to(source) or source.is_relative_to(target):
            raise ValueError("recursive_copy")
        excluded = {".git", "__pycache__", ".env", ".gradio", ".idea", *spec.get("exclude", [])}
        # Runtime media directories at the project root are not source. Nested
        # packages can legitimately be named input/output/models and must stay.
        excluded_paths = {"output", "outputs", "input", "temp", "tmp"}
        for value in spec.get("exclude_paths", []):
            path = Path(value)
            if path.is_absolute() or ".." in path.parts or not path.parts:
                raise ValueError("invalid_exclude_path")
            excluded_paths.add(path.as_posix())
        def path_excluded(path):
            relative = path.relative_to(source).as_posix()
            return any(relative == item or relative.startswith(item + "/") for item in excluded_paths)
        files = []
        if source.is_file():
            files.append((source, target))
        else:
            for directory, dirs, names in os.walk(source, followlinks=False):
                dirs[:]=[n for n in dirs if n not in excluded and not path_excluded(Path(directory) / n)]
                for child in dirs:
                    path = Path(directory) / child
                    if path.is_symlink() or getattr(path, "is_junction", lambda: False)():
                        raise ValueError("source_directory_link_requires_explicit_registration: " + str(path))
                for name in names:
                    path = Path(directory) / name
                    if name in excluded or path_excluded(path) or name.endswith((".log", ".session.json")) or name.startswith(".env"):
                        continue
                    if path.is_symlink():
                        raise ValueError("source_file_link_requires_explicit_registration: " + str(path))
                    files.append((path, target / path.relative_to(source)))
        for src, dest in files:
            count += 1
            size += src.stat().st_size
            if not apply:
                continue
            # Reject junction/symlink parents before opening any destination.
            for parent in [dest, *dest.parents]:
                if parent == root.parent:
                    break
                if parent.is_symlink() or getattr(parent, "is_junction", lambda: False)():
                    raise ValueError("destination_link_not_allowed")
            dest.parent.mkdir(parents=True, exist_ok=True)
            checksum = digest(src)
            if dest.exists():
                if digest(dest) != checksum:
                    raise ValueError("destination_conflict: " + str(dest))
                continue
            fd, path = tempfile.mkstemp(prefix=".migration-", dir=dest.parent)
            pending = Path(path)
            try:
                with os.fdopen(fd, "wb") as out, src.open("rb") as inp:
                    shutil.copyfileobj(inp, out, 4*1024*1024)
                    out.flush()
                    os.fsync(out.fileno())
                if digest(pending) != checksum or digest(src) != checksum:
                    raise ValueError("copy_integrity_failed")
                if dest.exists():
                    if digest(dest) != checksum: raise ValueError("destination_conflict")
                else: pending.replace(dest)
            finally:
                pending.unlink(missing_ok=True)
            if count % 2000 == 0:
                print(json.dumps({"verified_files": count, "bytes": size}), flush=True)
    return {"files": count, "bytes": size, "applied": apply, "originals_deleted": 0}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--plan", type=Path, required=True)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    print(json.dumps(migrate(json.loads(args.plan.read_text(encoding="utf-8-sig")), args.apply)), flush=True)
