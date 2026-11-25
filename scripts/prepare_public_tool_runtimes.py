"""Give installed Windows MCP tools their own embeddable Python interpreter.

The source catalog keeps binaries out of Git. The local build provisions each
installed tool after the verified Windows Python bundle has been prepared.
"""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import uuid


def digest(path):
    result = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1 << 20), b""):
            result.update(block)
    return result.hexdigest()


def prepare(release):
    release = release.resolve(strict=True)
    root = release / "publictools"
    if not root.is_dir():
        return []
    packages = []
    for package in sorted(root.iterdir()):
        descriptor = package / "plugin.json"
        if package.is_dir() and descriptor.is_file():
            metadata = json.loads(descriptor.read_text(encoding="utf-8-sig"))
            if metadata.get("runtime", {}).get("transport") == "mcp-stdio":
                packages.append((package, metadata))
    if not packages:
        return []
    source = release / "env/windows-python-cli"
    if not (source / "python.exe").is_file() or not (source / "python313.zip").is_file():
        raise ValueError("windows_python_bundle_missing")
    expected = digest(source / "python.exe")
    installed = []
    for package, metadata in packages:
        if metadata.get("runtime", {}).get("entrypoint") != "runtime/main.py":
            raise ValueError("unexpected_stdio_entrypoint:" + str(package))
        destination = package / "runtime/python"
        if destination.exists():
            if not (destination / "python313.zip").is_file() or digest(destination / "python.exe") != expected:
                raise ValueError("existing_package_python_differs:" + str(package))
        else:
            staging = package / ("runtime/python.staging-" + uuid.uuid4().hex)
            try:
                shutil.copytree(source, staging, ignore=shutil.ignore_patterns("__pycache__", "*.pyc"))
                if digest(staging / "python.exe") != expected:
                    raise ValueError("copied_package_python_differs:" + str(package))
                staging.rename(destination)
            finally:
                if staging.exists():
                    if not staging.resolve().is_relative_to(package.resolve(strict=True)):
                        raise ValueError("package_staging_outside_tool")
                    shutil.rmtree(staging)
        if metadata.get("id") == "document-workshop":
            native_source = release / "env/document-workshop"
            native = package / "runtime/native"
            if native_source.is_dir():
                marker = "runtime-manifest.json"
                source_hash = digest(native_source / marker)
                if native.exists():
                    if not (native / marker).is_file() or digest(native / marker) != source_hash:
                        raise ValueError("existing_document_runtime_differs")
                else:
                    staging = package / ("runtime/native.staging-" + uuid.uuid4().hex)
                    try:
                        shutil.copytree(native_source, staging, ignore=shutil.ignore_patterns("__pycache__", "*.pyc"))
                        if digest(staging / marker) != source_hash:
                            raise ValueError("copied_document_runtime_differs")
                        staging.rename(native)
                    finally:
                        if staging.exists():
                            if not staging.resolve().is_relative_to(package.resolve(strict=True)):
                                raise ValueError("package_staging_outside_tool")
                            shutil.rmtree(staging)
        installed.append(package.name)
    return installed


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--release", required=True, type=Path)
    args = parser.parse_args()
    print(json.dumps({"standalone_stdio_tools": prepare(args.release)}, ensure_ascii=False))
