#!/usr/bin/env python3
"""Validate a migration manifest against the actual release tree."""

from __future__ import annotations

import argparse
import json
from pathlib import Path


def load(path: Path) -> dict:
    value = json.loads(path.read_text(encoding="utf-8-sig"))
    if not isinstance(value, dict):
        raise ValueError("manifest must be an object")
    return value


def package_ids(root: Path, manifest_name: str) -> set[str]:
    ids: set[str] = set()
    if not root.is_dir():
        return ids
    for package in root.iterdir():
        if not package.is_dir():
            continue
        path = package / manifest_name
        if not path.is_file():
            continue
        try:
            value = load(path)
        except (OSError, UnicodeError, json.JSONDecodeError, ValueError):
            continue
        if isinstance(value.get("id"), str):
            ids.add(value["id"])
    return ids


def validate(release: Path, manifest_path: Path) -> dict[str, object]:
    manifest = load(manifest_path)
    errors: list[dict[str, str]] = []
    warnings: list[dict[str, str]] = []

    declared_apps = {item.get("id") for item in manifest.get("applications", []) if isinstance(item, dict)}
    declared_tools = {item.get("id") for item in manifest.get("public_tools", []) if isinstance(item, dict)}
    declared_data = {item.get("id") for item in manifest.get("tool_data", []) if isinstance(item, dict)}

    actual_apps = package_ids(release / "applications", "app.json")
    actual_tools = package_ids(release / "publictools", "plugin.json")
    actual_data = {item.name for item in (release / "data" / "tools").iterdir() if item.is_dir()} if (release / "data" / "tools").is_dir() else set()

    for label, declared, actual in (("application", declared_apps, actual_apps), ("public_tool", declared_tools, actual_tools), ("tool_data", declared_data, actual_data)):
        for item in sorted(declared - actual):
            errors.append({"type": label, "id": str(item), "code": "declared_missing"})
        for item in sorted(actual - declared):
            errors.append({"type": label, "id": str(item), "code": "actual_unlisted"})

    layout = manifest.get("layout", {})
    if not isinstance(layout, dict):
        errors.append({"type": "manifest", "code": "layout_not_object"})
    else:
        for key in ("applications", "public_tools", "runtimes", "data", "tool_data"):
            value = layout.get(key)
            if not isinstance(value, str) or Path(value).is_absolute() or ".." in Path(value).parts:
                errors.append({"type": "layout", "key": key, "code": "unsafe_or_missing_path"})

    return {
        "schema_version": 1,
        "release_root": str(release),
        "manifest": str(manifest_path),
        "ok": not errors,
        "errors": errors,
        "warnings": warnings,
        "counts": {
            "applications": len(actual_apps),
            "public_tools": len(actual_tools),
            "tool_data": len(actual_data),
        },
    }


def main() -> int:
    parser = argparse.ArgumentParser(description="校验发行包迁移清单")
    parser.add_argument("--release-root", required=True, type=Path)
    parser.add_argument("--manifest", type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    release = args.release_root.resolve()
    manifest = (args.manifest or release / "data" / "main_setting" / "registry" / "migration-manifest.json").resolve()
    result = validate(release, manifest)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"迁移清单校验：{'通过' if result['ok'] else '失败'}，错误 {len(result['errors'])} 个")
    return 0 if result["ok"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
