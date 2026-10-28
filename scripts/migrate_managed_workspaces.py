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
import stat
from migration_io import Directory, source_files, is_link


def digest(path):
    value = hashlib.sha256()
    if hasattr(path,'read'):
        path.seek(0)
        for chunk in iter(lambda:path.read(4*1024*1024),b''):value.update(chunk)
    else:
        with Directory(Path(path).parent) as parent, parent.open_regular(Path(path).name) as stream:
            return digest(stream)
    return value.hexdigest()


def marker(base):
    with Directory(base,create=True) as directory:
        try:
            with directory.open_regular('go.mod'):return
        except FileNotFoundError:pass
        pending,out=directory.temporary()
        try:
            with out:
                out.write(('module managed.local/'+base.name+'\n\ngo 1.25.0\n').encode())
                out.flush();os.fsync(out.fileno())
            try:directory.publish(pending,'go.mod')
            except FileExistsError:
                # A concurrently created custom module is preserved, not replaced.
                with directory.open_regular('go.mod'):pass
        finally:directory.remove(pending)


def copy_verified(src,dest):
    with Directory(src.parent) as source,Directory(dest.parent,create=True) as target:
        with source.open_regular(src.name) as inp:
            checksum=digest(inp)
            try:
                with target.open_regular(dest.name) as existing:
                    if digest(existing)!=checksum:raise ValueError('destination_conflict: '+str(dest))
                    target.still_named()
                    return os.fstat(existing.fileno()).st_size
            except FileNotFoundError:pass
            pending,out=target.temporary()
            try:
                with out:
                    inp.seek(0);shutil.copyfileobj(inp,out,4*1024*1024)
                    if os.name!='nt':
                        # Retain executable programs without copying setuid,
                        # setgid, or world/group write permissions.
                        os.fchmod(out.fileno(),0o700 if os.fstat(inp.fileno()).st_mode & 0o111 else 0o600)
                    out.flush();os.fsync(out.fileno())
                with target.open_regular(pending) as copied:
                    if digest(copied)!=checksum or digest(inp)!=checksum:raise ValueError('copy_integrity_failed')
                try:target.publish(pending,dest.name)
                except FileExistsError:
                    with target.open_regular(dest.name) as existing:
                        if digest(existing)!=checksum:raise ValueError('destination_conflict: '+str(dest))
                with target.open_regular(dest.name) as published:
                    if digest(published)!=checksum:raise ValueError('destination_conflict: '+str(dest))
                    size=os.fstat(published.fileno()).st_size
                target.still_named()
                return size
            finally:target.remove(pending)


def migrate(plan, apply=False):
    root = Path(plan["root"]).resolve(strict=True)
    approved = [root / "public_workspace", root / "pri_workspace"]
    if apply:
        for base in approved:
            if base.is_symlink() or getattr(base, "is_junction", lambda: False)(): raise ValueError("destination_link_not_allowed")
            marker(base)
    count = size = 0
    for spec in plan["copies"]:
        # Keep the registered name for actual I/O. Resolving a source link into
        # a new host path would silently broaden what the plan can read.
        source = Path(os.path.abspath(spec["source"]))
        with Directory(source.parent) as source_parent:
            source_info=source_parent.lstat(source.name)
            if is_link(source_info):raise ValueError('source_root_link_requires_physical_registration')
        source_canonical=source.resolve(strict=True)
        target = Path(spec["target"])
        if not target.is_absolute():
            target = root / target
        for parent in [target, *target.parents]:
            if parent.is_symlink() or getattr(parent, "is_junction", lambda: False)():
                raise ValueError("destination_link_not_allowed")
        target = target.resolve()
        if not any(target.is_relative_to(base) and target != base for base in approved):
            raise ValueError("target_outside_managed_workspace")
        if target.is_relative_to(source_canonical) or source_canonical.is_relative_to(target):
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
        files = []
        if stat.S_ISREG(source_info.st_mode):
            files.append((source, target, source_info.st_size))
        elif stat.S_ISDIR(source_info.st_mode):
            files=[(path,target/relative,size) for path,relative,size in source_files(source,excluded,excluded_paths)]
        else:
            raise ValueError('source_not_regular')
        for src, dest, file_size in files:
            count += 1
            if not apply:
                size += file_size
                continue
            # Reject junction/symlink parents before opening any destination.
            for parent in [dest, *dest.parents]:
                if parent == root.parent:
                    break
                if parent.is_symlink() or getattr(parent, "is_junction", lambda: False)():
                    raise ValueError("destination_link_not_allowed")
            size += copy_verified(src,dest)
            if count % 2000 == 0:
                print(json.dumps({"verified_files": count, "bytes": size}), flush=True)
    return {"files": count, "bytes": size, "applied": apply, "originals_deleted": 0}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--plan", type=Path, required=True)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    print(json.dumps(migrate(json.loads(args.plan.read_text(encoding="utf-8-sig")), args.apply)), flush=True)
