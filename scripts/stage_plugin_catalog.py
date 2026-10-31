"""Stage only verified official plugin source files into a portable catalog.

The catalog intentionally contains package descriptors and their indexed source
only. It never packages Python environments, models, account data, generated
outputs, or installed plugin state.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import sys


IDENTIFIER = re.compile(r"^[a-z][a-z0-9-]{0,63}$")
# These are host-side declarative metadata tests/source, deliberately adjacent
# to package directories. They are not plugin package members and must never be
# copied into a portable runtime catalog.
ROOT_METADATA_FILES = {"catalog.go", "catalog_test.go"}


class StageError(ValueError):
    """A safe, path-free staging failure."""


def _regular(path):
    return path.is_file() and not path.is_symlink()


def _safe_member(name):
    if not isinstance(name, str) or not name or "\\" in name or ":" in name:
        return False
    member = PurePosixPath(name)
    return not member.is_absolute() and "." not in member.parts and ".." not in member.parts and str(member) == name and name != "plugin.json"


def _digest(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for part in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(part)
    return digest.hexdigest()


def _read_package(directory):
    if directory.is_symlink() or not directory.is_dir() or not IDENTIFIER.fullmatch(directory.name):
        raise StageError("invalid_plugin_directory")
    manifest_path = directory / "plugin.json"
    if not _regular(manifest_path):
        raise StageError("plugin_manifest_missing")
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf8"))
        indexed = manifest["files"]
    except (OSError, UnicodeError, ValueError, KeyError, TypeError):
        raise StageError("invalid_plugin_manifest") from None
    if manifest.get("id") != directory.name or not isinstance(indexed, dict) or not indexed:
        raise StageError("invalid_plugin_manifest")
    expected = {"plugin.json"}
    for member, digest in indexed.items():
        if not _safe_member(member) or not isinstance(digest, str) or not re.fullmatch(r"[0-9a-f]{64}", digest):
            raise StageError("invalid_plugin_manifest")
        path = directory.joinpath(*PurePosixPath(member).parts)
        if not _regular(path) or _digest(path) != digest:
            raise StageError("plugin_manifest_hash_mismatch")
        expected.add(member)
    found = set()
    for current, directories, files in os.walk(directory, followlinks=False):
        root = Path(current)
        if any((root / child).is_symlink() for child in directories + files):
            raise StageError("plugin_links_forbidden")
        for filename in files:
            path = root / filename
            if not _regular(path):
                raise StageError("plugin_special_file_forbidden")
            found.add(path.relative_to(directory).as_posix())
    if found != expected:
        raise StageError("plugin_contains_unindexed_files")
    return sorted(expected)


def _catalog(source):
    if source.is_symlink() or not source.is_dir():
        raise StageError("source_catalog_unavailable")
    entries = list(source.iterdir())
    if not entries:
        raise StageError("source_catalog_empty")
    directories = []
    for entry in entries:
        if entry.is_symlink():
            raise StageError("source_catalog_contains_non_package")
        if entry.is_dir():
            directories.append(entry)
            continue
        if not _regular(entry) or entry.name not in ROOT_METADATA_FILES:
            raise StageError("source_catalog_contains_non_package")
    if not directories:
        raise StageError("source_catalog_empty")
    packages = {}
    for directory in sorted(directories, key=lambda value: value.name):
        packages[directory.name] = _read_package(directory)
    return packages


def _same_file(left, right):
    if not _regular(left) or not _regular(right) or left.stat().st_size != right.stat().st_size:
        return False
    return _digest(left) == _digest(right)


def _existing_matches(source, output, packages):
    if output.is_symlink() or not output.is_dir():
        return False
    expected = set()
    for package, members in packages.items():
        expected.update(f"{package}/{member}" for member in members)
    found = set()
    for current, directories, files in os.walk(output, followlinks=False):
        root = Path(current)
        if any((root / child).is_symlink() for child in directories + files):
            return False
        for filename in files:
            path = root / filename
            if not _regular(path):
                return False
            found.add(path.relative_to(output).as_posix())
    if found != expected:
        return False
    return all(_same_file(source / relative, output / relative) for relative in expected)


def _overlaps(source, output):
    """Return whether either catalog path contains the other, without writing."""
    try:
        source_resolved = source.resolve(strict=False)
        output_resolved = output.resolve(strict=False)
        output_resolved.relative_to(source_resolved)
        return True
    except ValueError:
        try:
            source_resolved.relative_to(output_resolved)
            return True
        except ValueError:
            return False


def stage(source, output):
    """Validate source catalog then create it once, or prove existing equality."""
    # Do not resolve here: resolving the catalog root would hide a symlink
    # before the no-links validation below can reject it.
    source, output = Path(source).absolute(), Path(output).absolute()
    # A nested output would become an unindexed source member during a later
    # invocation. Reject all equal/ancestor relations before mkdir/copyfile.
    if _overlaps(source, output):
        raise StageError("source_output_overlap")
    packages = _catalog(source)
    if output.exists():
        if output.is_symlink() or not output.is_dir():
            raise StageError("output_requires_fresh_directory")
        if any(output.iterdir()):
            if not _existing_matches(source, output, packages):
                raise StageError("output_requires_fresh_directory")
            return {"packages": sorted(packages), "files": sum(len(items) for items in packages.values()), "idempotent": True}
    output.mkdir(parents=True, exist_ok=True)
    for package, members in packages.items():
        for member in members:
            destination = output / package / Path(*PurePosixPath(member).parts)
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source / package / Path(*PurePosixPath(member).parts), destination)
    return {"packages": sorted(packages), "files": sum(len(items) for items in packages.values()), "idempotent": False}


def main(argv=None):
    parser = argparse.ArgumentParser(description="stage verified official plugin source catalog")
    parser.add_argument("--source", required=True, help="official plugin source directory, for example plugins")
    parser.add_argument("--output", required=True, help="new catalog directory, for example OUTPUT/plugins")
    args = parser.parse_args(argv)
    try:
        result = stage(args.source, args.output)
    except StageError as error:
        print(str(error), file=sys.stderr)
        return 1
    print(json.dumps(result, ensure_ascii=False, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
