"""Prepare a pinned, project-local headless Python for the Windows broker.

The upstream ZIP remains cached unchanged. CLI copies omit PE manifest
resources: Python's GUI/SxS startup otherwise fails inside LPAC before main.
No code, DLL imports, sandbox capabilities or system ACLs are changed.
"""
import argparse
import ctypes
from ctypes import wintypes
import hashlib
import json
import os
from pathlib import Path
import shutil
import urllib.request
import uuid
import zipfile

VERSION = "3.13.13"
SHA256 = "8766a8775746235e23cf5aee5027ab1060bb981d93110577adcf3508aa0cbd55"
URL = f"https://www.python.org/ftp/python/{VERSION}/python-{VERSION}-embed-amd64.zip"


def digest(path):
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def remove_startup_manifest(path, resource):
    kernel = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel.LoadLibraryExW.argtypes = [wintypes.LPCWSTR, wintypes.HANDLE, wintypes.DWORD]
    kernel.LoadLibraryExW.restype = wintypes.HANDLE
    kernel.FindResourceW.argtypes = [wintypes.HANDLE, ctypes.c_void_p, ctypes.c_void_p]
    kernel.FindResourceW.restype = wintypes.HANDLE
    kernel.FreeLibrary.argtypes = [wintypes.HANDLE]
    image = kernel.LoadLibraryExW(str(path), None, 0x22)  # data/image only; never executes code
    if not image:
        raise ctypes.WinError(ctypes.get_last_error())
    try:
        present = bool(kernel.FindResourceW(image, ctypes.c_void_p(resource), ctypes.c_void_p(24)))
    finally:
        kernel.FreeLibrary(image)
    if not present:
        return False
    kernel.BeginUpdateResourceW.argtypes = [wintypes.LPCWSTR, wintypes.BOOL]
    kernel.BeginUpdateResourceW.restype = wintypes.HANDLE
    kernel.UpdateResourceW.argtypes = [wintypes.HANDLE, ctypes.c_void_p, ctypes.c_void_p,
                                     wintypes.WORD, ctypes.c_void_p, wintypes.DWORD]
    kernel.EndUpdateResourceW.argtypes = [wintypes.HANDLE, wintypes.BOOL]
    handle = kernel.BeginUpdateResourceW(str(path), False)
    if not handle:
        raise ctypes.WinError(ctypes.get_last_error())
    try:
        # These exact resources/language belong to the hash-pinned ZIP.
        if not kernel.UpdateResourceW(handle, ctypes.c_void_p(24), ctypes.c_void_p(resource),
                                      1033, None, 0):
            raise ctypes.WinError(ctypes.get_last_error())
    except BaseException:
        kernel.EndUpdateResourceW(handle, True)
        raise
    if not kernel.EndUpdateResourceW(handle, False):
        raise ctypes.WinError(ctypes.get_last_error())
    return True


def prepare(target, cache):
    if os.name != "nt":
        raise RuntimeError("Windows runtime preparation requires Windows")
    target, cache = target.absolute(), cache.absolute()
    if target.exists():
        record = json.loads((target / "runtime-manifest.json").read_text(encoding="utf8"))
        if record.get("upstream_sha256") != SHA256 or record.get("transform") != "headless-pe-manifests-v2":
            raise RuntimeError("Existing runtime has a different identity; no overwrite performed")
        if any(digest(target / name) != value for name, value in record["files"].items()):
            raise RuntimeError("Existing runtime integrity check failed; no overwrite performed")
        return record
    cache.mkdir(parents=True, exist_ok=True)
    archive = cache / URL.rsplit("/", 1)[-1]
    if not archive.exists():
        pending = archive.with_suffix(".pending")
        with urllib.request.urlopen(URL, timeout=60) as response, pending.open("wb") as out:
            shutil.copyfileobj(response, out)
        if digest(pending) != SHA256:
            raise RuntimeError("Python archive checksum mismatch")
        pending.replace(archive)
    if digest(archive) != SHA256:
        raise RuntimeError("Python archive checksum mismatch")
    target.parent.mkdir(parents=True, exist_ok=True)
    # Python 3.13's mkdtemp deliberately creates a protected Windows DACL.
    # Public runtimes need normal inheritance so a broker's read-only mount
    # can reach their DLLs; write access still follows the managed parent ACL.
    stage = target.parent / (target.name + ".staging-" + uuid.uuid4().hex)
    stage.mkdir(mode=0o755)
    try:
        with zipfile.ZipFile(archive) as source:
            for info in source.infolist():
                name = info.filename.replace("\\", "/")
                if name.startswith("/") or ":" in name or ".." in name.split("/"):
                    raise RuntimeError("Unsafe runtime archive member")
            source.extractall(stage)
        for binary in stage.iterdir():
            if binary.suffix.lower() in {".exe", ".dll", ".pyd"}:
                remove_startup_manifest(binary, 1 if binary.suffix.lower() == ".exe" else 2)
        record = {"version": VERSION, "platform": "windows-x64", "upstream_url": URL,
                  "upstream_sha256": SHA256, "transform": "headless-pe-manifests-v2",
                  "description": "CLI-only derivative; upstream publisher signatures no longer apply",
                  "files": {str(p.relative_to(stage)): digest(p) for p in stage.rglob("*") if p.is_file()}}
        (stage / "runtime-manifest.json").write_text(json.dumps(record, indent=2), encoding="utf8")
        stage.rename(target)
        return record
    finally:
        if stage.exists():
            shutil.rmtree(stage)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", type=Path, required=True)
    parser.add_argument("--cache", type=Path, required=True)
    options = parser.parse_args()
    result = prepare(options.target, options.cache)
    print(json.dumps({"ready": True, "version": result["version"], "target": str(options.target.absolute())}))
