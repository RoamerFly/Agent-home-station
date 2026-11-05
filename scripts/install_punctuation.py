"""Install the optional offline punctuation runtime beside wechat-admin."""
import argparse
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile

ARCHIVE_SHA256 = "c0d5aa5f8eeb686032345e180bedf39319dc2e0556781c6264bcadba8328a6e1"
MODEL = "sherpa-onnx-punct-ct-transformer-zh-en-vocab272727-2024-04-12-int8"


def install(archive, admin_dir, wheels=None):
    with archive.open("rb") as stream:
        if hashlib.file_digest(stream, "sha256").hexdigest() != ARCHIVE_SHA256:
            raise ValueError("unexpected punctuation archive digest")
    destination = admin_dir / "punctuation"
    if destination.exists():
        raise ValueError("punctuation already installed; select an empty staging directory")
    if os.name == "nt":
        interpreter = admin_dir.parent / "windows-python-cli" / "python.exe"
        version = subprocess.check_output([interpreter, "-I", "-c", "import sys;print(sys.implementation.cache_tag)"], text=True).strip()
        if version != sys.implementation.cache_tag:
            raise ValueError("build Python must match the portable interpreter")
    with tempfile.TemporaryDirectory(dir=admin_dir) as temp:
        stage = Path(temp) / "punctuation"
        stage.mkdir()
        with tarfile.open(archive, "r:bz2") as bundle:
            for name in ("model.int8.onnx", "README.md"):
                member = bundle.getmember(MODEL + "/" + name)
                if not member.isfile():
                    raise ValueError("invalid model archive")
                with bundle.extractfile(member) as source, (stage / name).open("xb") as target:
                    shutil.copyfileobj(source, target)
        options = ["--no-index", "--find-links", str(wheels)] if wheels else []
        subprocess.run([sys.executable, "-m", "pip", "install", "--no-deps", "--only-binary=:all:",
                        "--target", str(stage / "packages"), *options, "sherpa-onnx==2.0.1",
                        "sherpa-onnx-core==2.0.1", "numpy==2.2.6"], check=True)
        if os.name != "nt":
            (stage / "bin").mkdir()
            (stage / "bin/python").symlink_to(Path(sys.executable).resolve())
        stage.rename(destination)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", required=True, type=Path)
    parser.add_argument("--admin-dir", required=True, type=Path)
    parser.add_argument("--wheel-dir", type=Path)
    args = parser.parse_args()
    install(args.archive, args.admin_dir.resolve(strict=True), args.wheel_dir)
