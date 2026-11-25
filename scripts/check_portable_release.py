#!/usr/bin/env python3
"""Validate a Windows Agent-home-station release package.

The checker is intentionally read-only. It validates the immutable package
areas and never scans or prints private data under ``data``.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from pathlib import Path, PurePosixPath


TOOL_ID = re.compile(r"^[a-z][a-z0-9-]{0,63}$")
APP_ID = TOOL_ID
REQUIRED_ROOTS = ("applications", "publictools", "env", "data")
SENSITIVE_NAME = re.compile(
    r"^(?:\.env(?:\..*)?|(?:api[-_]?key|access[-_]?token|secret|cookie)"
    r"\.(?:txt|json|yaml|yml|env)|.*\.session(?:\.json)?|"
    r".*\.sqlite3?|.*\.db)$",
    re.IGNORECASE,
)


def digest(path: Path) -> str:
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            value.update(chunk)
    return value.hexdigest()


def safe_relative(value: object) -> str | None:
    if not isinstance(value, str) or not value.strip():
        return None
    candidate = value.replace("\\", "/")
    path = PurePosixPath(candidate)
    if path.is_absolute() or ":" in candidate or ".." in path.parts:
        return None
    return candidate


def load_json(path: Path, errors: list[dict[str, str]]) -> dict | None:
    try:
        value = json.loads(path.read_text(encoding="utf-8-sig"))
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        errors.append({"path": str(path), "code": "invalid_json", "detail": type(exc).__name__})
        return None
    if not isinstance(value, dict):
        errors.append({"path": str(path), "code": "manifest_not_object"})
        return None
    return value


def validate_files(root: Path, manifest: dict, errors: list[dict[str, str]], warnings: list[dict[str, str]]) -> None:
    files = manifest.get("files")
    if files is None:
        warnings.append({"path": str(root), "code": "manifest_has_no_file_index"})
        return
    if not isinstance(files, dict):
        errors.append({"path": str(root), "code": "file_index_not_object"})
        return
    for relative, expected in files.items():
        safe = safe_relative(relative)
        if safe is None:
            errors.append({"path": str(root), "code": "unsafe_manifest_member"})
            continue
        target = root / Path(*safe.split("/"))
        if not target.is_file():
            errors.append({"path": str(target), "code": "missing_manifest_member"})
            continue
        if not isinstance(expected, str) or digest(target) != expected.lower():
            errors.append({"path": str(target), "code": "manifest_digest_mismatch"})


def validate_package(root: Path, manifest_name: str, ids: set[str], errors: list[dict[str, str]], warnings: list[dict[str, str]]) -> dict[str, object]:
    manifest_path = root / manifest_name
    manifest = load_json(manifest_path, errors)
    if manifest is None:
        return {"path": str(root), "status": "invalid"}
    package_id = manifest.get("id")
    if not isinstance(package_id, str) or not TOOL_ID.fullmatch(package_id):
        errors.append({"path": str(manifest_path), "code": "invalid_id"})
    elif package_id in ids:
        errors.append({"path": str(manifest_path), "code": "duplicate_id"})
    else:
        ids.add(package_id)
    entrypoint = ((manifest.get("runtime") or {}).get("entrypoint"))
    if entrypoint is not None:
        safe = safe_relative(entrypoint)
        if safe is None or not (root / Path(*safe.split("/"))).is_file():
            errors.append({"path": str(manifest_path), "code": "missing_or_absolute_entrypoint"})
    validate_files(root, manifest, errors, warnings)
    return {"id": package_id, "path": str(root), "version": manifest.get("version"), "status": manifest.get("status")}


def inspect_package_tree(root: Path, errors: list[dict[str, str]], warnings: list[dict[str, str]]) -> int:
    count = 0
    for path in root.rglob("*"):
        if not path.is_file():
            continue
        try:
            relative = path.relative_to(root)
        except ValueError:
            continue
        if any(SENSITIVE_NAME.search(part) for part in relative.parts):
            errors.append({"path": str(path), "code": "sensitive_file_in_package"})
        count += 1
    return count


def check_release(release: Path) -> dict[str, object]:
    errors: list[dict[str, str]] = []
    warnings: list[dict[str, str]] = []
    for name in REQUIRED_ROOTS:
        if not (release / name).is_dir():
            errors.append({"path": str(release / name), "code": "missing_required_directory"})
    if not (release / "AgentHomeStation.exe").is_file():
        errors.append({"path": str(release / "AgentHomeStation.exe"), "code": "missing_launcher"})

    tools: list[dict[str, object]] = []
    tool_ids: set[str] = set()
    public_root = release / "publictools"
    if public_root.is_dir():
        for package in sorted((item for item in public_root.iterdir() if item.is_dir()), key=lambda item: item.name.lower()):
            manifest = package / "plugin.json"
            if not manifest.is_file():
                warnings.append({"path": str(package), "code": "tool_manifest_missing"})
                continue
            tools.append(validate_package(package, "plugin.json", tool_ids, errors, warnings))
        package_files = inspect_package_tree(public_root, errors, warnings)
    else:
        package_files = 0

    applications: list[dict[str, object]] = []
    app_ids: set[str] = set()
    app_root = release / "applications"
    if app_root.is_dir():
        for package in sorted((item for item in app_root.iterdir() if item.is_dir()), key=lambda item: item.name.lower()):
            manifest = package / "app.json"
            if not manifest.is_file():
                warnings.append({"path": str(package), "code": "application_manifest_missing"})
                continue
            applications.append(validate_package(package, "app.json", app_ids, errors, warnings))
        application_files = inspect_package_tree(app_root, errors, warnings)
    else:
        application_files = 0

    return {
        "schema_version": 1,
        "release_root": str(release),
        "ok": not errors,
        "errors": errors,
        "warnings": warnings,
        "summary": {
            "applications": len(applications),
            "public_tools": len(tools),
            "application_files": application_files,
            "public_tool_files": package_files,
        },
        "applications": applications,
        "public_tools": tools,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description="检查 Agent-home-station Windows 发行包")
    parser.add_argument("--release-root", required=True, type=Path)
    parser.add_argument("--json", dest="json_path", type=Path)
    parser.add_argument("--write-manifest", action="store_true", help="校验通过后写入 release/manifest.json")
    args = parser.parse_args()
    report = check_release(args.release_root.resolve())
    payload = json.dumps(report, ensure_ascii=False, indent=2) + "\n"
    if args.json_path:
        args.json_path.parent.mkdir(parents=True, exist_ok=True)
        args.json_path.write_text(payload, encoding="utf-8")
    summary = report["summary"]
    print(
        "发行包检查：{}，应用 {} 个，公共工具 {} 个，应用文件 {} 个，工具文件 {} 个，错误 {} 个，警告 {} 个".format(
            "通过" if report["ok"] else "失败",
            summary["applications"],
            summary["public_tools"],
            summary["application_files"],
            summary["public_tool_files"],
            len(report["errors"]),
            len(report["warnings"]),
        )
    )
    for item in report["errors"][:20]:
        print("错误：{}：{}".format(item.get("code", "unknown"), item.get("path", "")))
    if len(report["errors"]) > 20:
        print("错误：其余 {} 项已写入 JSON 报告".format(len(report["errors"]) - 20))
    if args.write_manifest and report["ok"]:
        release = args.release_root.resolve()

        def relative(item: dict[str, object]) -> dict[str, object]:
            value = dict(item)
            value["path"] = Path(str(value["path"])).resolve().relative_to(release).as_posix()
            return value

        manifest = {
            "schema_version": 1,
            "package": "agent-home-station",
            "platform": "windows-amd64",
            "layout": {
                "applications": "applications",
                "public_tools": "publictools",
                "runtimes": "env",
                "data": "data",
            },
            "applications": [relative(item) for item in report["applications"]],
            "public_tools": [relative(item) for item in report["public_tools"]],
        }
        (release / "manifest.json").write_text(
            json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
        )
        print("已写入：{}".format(release / "manifest.json"))
    return 0 if report["ok"] else 1


if __name__ == "__main__":
    sys.exit(main())
