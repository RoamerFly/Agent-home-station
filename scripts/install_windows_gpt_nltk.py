"""Install hash-pinned NLTK pronunciation data into the managed public runtime.

This is a one-time owner-side preparation step. The LPAC inference worker stays
offline and sees only the installed read-only data. No user voice is uploaded.
"""
import argparse
import hashlib
from pathlib import Path
import shutil
import urllib.request
import zipfile


PACKAGES = {
    "corpora/cmudict": "d07cca47fd72ad32ea9d8ad1219f85301eeaf4568f8b6b73747506a71fb5afd6",
    "tokenizers/punkt_tab": "e57f64187974277726a3417ca6f181ec5403676c717672eef6a748a7b20e0106",
    "taggers/averaged_perceptron_tagger_eng": "6025f530624335c67d6547d44757b357b4e79bae030a0383e9887a92c1718f0b",
    "taggers/averaged_perceptron_tagger": "e1f13cf2532daadfd6f3bc481a49859f0b8ea6432ccdcd83e6a49a5f19008de9",
}
BASE = "https://raw.githubusercontent.com/nltk/nltk_data/gh-pages/packages/"


def install(destination: Path, cache: Path):
    destination = destination.resolve()
    cache.mkdir(parents=True, exist_ok=True)
    for key, expected in PACKAGES.items():
        category, name = key.split("/", 1)
        target = destination / category / name
        marker = target / ".wechat-nltk-sha256"
        if target.exists():
            if not marker.is_file() or marker.read_text(encoding="ascii").strip() != expected:
                raise RuntimeError("existing_nltk_resource_requires_review: " + key)
        archive = cache / (name + ".zip")
        if not archive.exists():
            pending = archive.with_suffix(".pending")
            with urllib.request.urlopen(BASE + key + ".zip", timeout=60) as response, pending.open("wb") as output:
                shutil.copyfileobj(response, output)
            if hashlib.sha256(pending.read_bytes()).hexdigest() != expected:
                pending.unlink(missing_ok=True)
                raise RuntimeError("nltk_resource_checksum_mismatch: " + key)
            pending.replace(archive)
        if hashlib.sha256(archive.read_bytes()).hexdigest() != expected:
            raise RuntimeError("nltk_resource_checksum_mismatch: " + key)
        # g2p_en probes the ZIP path explicitly before importing its English
        # tokenizer; keeping only extracted resources still triggers a runtime
        # download attempt from inside the offline sandbox.
        installed_zip = destination / category / (name + ".zip")
        if installed_zip.exists():
            if hashlib.sha256(installed_zip.read_bytes()).hexdigest() != expected:
                raise RuntimeError("existing_nltk_archive_requires_review: " + key)
        else:
            installed_zip.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(archive, installed_zip)
        if target.exists():
            continue
        with zipfile.ZipFile(archive) as source:
            for member in source.infolist():
                parts = Path(member.filename.replace("\\", "/")).parts
                if not parts or parts[0] != name or ".." in parts or member.filename.startswith("/"):
                    raise RuntimeError("unsafe_nltk_archive_member: " + key)
            target.parent.mkdir(parents=True, exist_ok=True)
            source.extractall(target.parent)
        marker.write_text(expected + "\n", encoding="ascii")
    return destination


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--destination", required=True, type=Path)
    parser.add_argument("--cache", required=True, type=Path)
    options = parser.parse_args()
    print(install(options.destination, options.cache))
