#!/usr/bin/env python3
"""Build a portable migration manifest without reading private values."""

from __future__ import annotations

import argparse
import json
from pathlib import Path


def read_manifest(path: Path) -> dict[str, object] | None:
    try:
        value = json.loads(path.read_text(encoding="utf-8-sig"))
    except (OSError, UnicodeError, json.JSONDecodeError):
        return None
    return value if isinstance(value, dict) else None


def package_rows(root: Path, manifest_name: str) -> list[dict[str, object]]:
    rows: list[dict[str, object]] = []
    if not root.is_dir():
        return rows
    for package in sorted((item for item in root.iterdir() if item.is_dir()), key=lambda item: item.name.lower()):
        manifest = read_manifest(package / manifest_name)
        if manifest is None:
            continue
        rows.append(
            {
                "id": manifest.get("id"),
                "name": manifest.get("name"),
                "version": manifest.get("version"),
                "status": manifest.get("status"),
                "path": package.relative_to(root.parent).as_posix(),
                "runtime": manifest.get("runtime", {}),
            }
        )
    return rows


def data_rows(data_root: Path) -> list[dict[str, object]]:
    rows: list[dict[str, object]] = []
    tools = data_root / "tools"
    if not tools.is_dir():
        return rows
    for tool in sorted((item for item in tools.iterdir() if item.is_dir()), key=lambda item: item.name.lower()):
        categories: dict[str, dict[str, int]] = {}
        for category in ("config", "state", "jobs", "cache", "logs"):
            directory = tool / category
            if not directory.is_dir():
                continue
            files = [item for item in directory.rglob("*") if item.is_file()]
            categories[category] = {
                "files": len(files),
                "bytes": sum(item.stat().st_size for item in files),
            }
        root_files = [item for item in tool.iterdir() if item.is_file()]
        if root_files:
            categories["root"] = {
                "files": len(root_files),
                "bytes": sum(item.stat().st_size for item in root_files),
            }
        rows.append(
            {
                "id": tool.name,
                "path": tool.relative_to(data_root).as_posix(),
                "categories": categories,
            }
        )
    return rows


def build(release: Path) -> dict[str, object]:
    data = release / "data"
    return {
        "schema_version": 1,
        "kind": "agent-home-station-migration",
        "platform": "windows-amd64",
        "layout": {
            "release_root": ".",
            "applications": "applications",
            "public_tools": "publictools",
            "runtimes": "env",
            "data": "data",
            "tool_data": "data/tools",
        },
        "applications": package_rows(release / "applications", "app.json"),
        "public_tools": package_rows(release / "publictools", "plugin.json"),
        "tool_data": data_rows(data),
        "system_data": [
            "data/publictools/catalog",
            "data/publictools/registrations",
            "data/publictools/validation",
        ],
        "private_data": {
            "excluded_from_manifest": [
                "login/session material",
                "API keys and tokens",
                "chat message bodies",
                "browser profiles",
            ]
        },
    }


def main() -> int:
    parser = argparse.ArgumentParser(description="生成 Agent-home-station 迁移清单")
    parser.add_argument("--release-root", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    result = build(args.release_root.resolve())
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"迁移清单已生成：应用 {len(result['applications'])} 个，公共工具 {len(result['public_tools'])} 个，工具数据 {len(result['tool_data'])} 个")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
