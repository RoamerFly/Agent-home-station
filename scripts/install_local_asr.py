"""Install a verified SenseVoice ONNX archive into the public model workspace.

Run using the intended managed Python. The archive is downloaded separately;
this step verifies its published SHA-256, extracts only regular files into a
staging directory, installs a pinned CPU SDK without modifying music packages,
and atomically registers project/model paths. No credentials are involved.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile

SHA256 = "7305f7905bfcf77fa0b39388a313f3da35c68d971661a65475b56fb2162c8e63"
SDK_VERSION = "2.0.1"
VAD_SHA256 = "9e2449e1087496d8d4caba907f23e0bd3f78d91fa552479bb9c23ac09cbb1fd6"


def digest_file(path):
    h=hashlib.sha256()
    with Path(path).open('rb') as stream:
        for block in iter(lambda:stream.read(1024*1024),b''):
            h.update(block)
    return h.hexdigest()


def install(workspace, archive, wheel_dir=None):
    workspace, archive = Path(workspace).resolve(strict=True), Path(archive).resolve(strict=True)
    public = workspace / "public_workspace"
    if public.is_symlink():
        raise ValueError("public_workspace_is_link")
    digest = digest_file(archive)
    if digest != SHA256:
        raise ValueError("asr_archive_hash_mismatch")
    models = public / "models/asr"
    models.mkdir(parents=True, exist_ok=True)
    destination = models / "sensevoice-small-int8"
    staging = Path(tempfile.mkdtemp(prefix="sensevoice-install-", dir=models))
    try:
        with tarfile.open(archive, "r:bz2") as bundle:
            for member in bundle:
                relative = Path(*Path(member.name).parts[1:])
                if member.isdir():
                    continue
                if not member.isfile() or relative.is_absolute() or ".." in relative.parts or relative == Path("."):
                    raise ValueError("unsafe_model_archive")
                if relative.name not in {"model.int8.onnx", "tokens.txt", "LICENSE", "README.md"} and relative.parts[0] != "test_wavs":
                    continue
                target = staging / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                with bundle.extractfile(member) as source, target.open("xb") as output:
                    shutil.copyfileobj(source, output)
        for filename in ("model.int8.onnx", "tokens.txt"):
            if not (staging / filename).is_file():
                raise ValueError("asr_archive_required_file_missing")
        if destination.exists():
            for source in staging.rglob("*"):
                if source.is_file():
                    existing = destination / source.relative_to(staging)
                    if not existing.is_file() or digest_file(existing) != digest_file(source):
                        raise ValueError("existing_asr_model_differs_preserved")
        else:
            staging.replace(destination)
        packages = public / "runtimes/sherpa-onnx-cpu"
        if not (packages / "sherpa_onnx").is_dir():
            pending = Path(tempfile.mkdtemp(prefix="sherpa-install-", dir=packages.parent))
            try:
                options=["--no-index", "--find-links", str(Path(wheel_dir).resolve(strict=True))] if wheel_dir else []
                subprocess.run([sys.executable, "-m", "pip", "install", "--disable-pip-version-check", "--no-deps", "--timeout", "30", "--retries", "2",
                                "--target", str(pending), *options, "sherpa-onnx==" + SDK_VERSION, "sherpa-onnx-core=="+SDK_VERSION], check=True, timeout=300)
                if packages.exists():
                    raise ValueError("existing_sherpa_directory_preserved")
                pending.replace(packages)
            finally:
                if pending.exists():
                    shutil.rmtree(pending)
        scripts = public / "projects/agent-tools"
        scripts.mkdir(parents=True, exist_ok=True)
        entry = scripts / "local_asr.py"
        shutil.copyfile(Path(__file__).with_name("local_asr.py"), entry)
        vad = models / "silero_vad.onnx"
        vad_download = models / "downloads/silero_vad.onnx"
        if not vad.exists() and vad_download.is_file():
            if hashlib.sha256(vad_download.read_bytes()).hexdigest() != VAD_SHA256:
                raise ValueError("vad_hash_mismatch")
            shutil.copyfile(vad_download, vad)
        # Keep the venv path, not the symlink-resolved base interpreter: the
        # managed music venv already provides NumPy, which the decoder uses.
        python = str(Path(sys.executable).absolute())
        manifest = {"id": "asr-sensevoice-small-int8", "name": "SenseVoice Small INT8 本地语音识别",
                    "root": str(destination), "python": python, "entry": str(entry), "packages": str(packages),
                    "model": str(destination / "model.int8.onnx"), "tokens": str(destination / "tokens.txt"),
                    "threads": 2, "language": "auto", "chunk_seconds": 30, "timeout_seconds": 120,
                    "archive_sha256": SHA256, "sdk_version": SDK_VERSION,
                    "usage": "CPU离线转写。输入16k单声道PCM16 WAV；--threads、--language、--chunk-seconds可调。程序、模型、SDK供账号共用；输入输出保存在当前账号私有目录，不需要令牌。"}
        if vad.is_file():
            manifest.update(vad_model=str(vad), vad_threshold=.5, vad_sha256=VAD_SHA256)
        manifests = public / "manifests"
        manifests.mkdir(exist_ok=True)
        target = manifests / "asr-sensevoice-small-int8.json"
        if not target.exists():
            target.write_text(json.dumps(manifest, ensure_ascii=False, indent=2), encoding="utf-8")
        print(json.dumps({"installed": True, "model_bytes": (destination / "model.int8.onnx").stat().st_size,
                          "sdk_version": SDK_VERSION, "manifest": str(target)}))
    finally:
        if staging.exists():
            shutil.rmtree(staging)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--workspace", required=True)
    parser.add_argument("--archive", required=True)
    parser.add_argument("--wheel-dir")
    arguments = parser.parse_args()
    install(arguments.workspace, arguments.archive, arguments.wheel_dir)
