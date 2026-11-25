#!/usr/bin/env python3
"""Create a read-only migration preview for mutable tool data."""

from __future__ import annotations

import argparse
import json
from pathlib import Path


CATEGORIES = ("config", "state", "jobs", "cache", "logs")
SYSTEM_DATA_DIRS = {"catalog", "registrations", "validation"}


def size_and_files(path: Path) -> tuple[int, int]:
    if not path.is_dir():
        return 0, 0
    total = 0
    files = 0
    for item in path.rglob("*"):
        if item.is_file():
            files += 1
            try:
                total += item.stat().st_size
            except OSError:
                pass
    return files, total


def discover_ids(release_root: Path | None) -> dict[str, str]:
    if release_root is None:
        return {}
    result: dict[str, str] = {}
    public = release_root / "publictools"
    if not public.is_dir():
        return result
    for package in public.iterdir():
        manifest = package / "plugin.json"
        if not manifest.is_file():
            continue
        try:
            value = json.loads(manifest.read_text(encoding="utf-8-sig"))
        except (OSError, UnicodeError, json.JSONDecodeError):
            continue
        if isinstance(value, dict) and isinstance(value.get("id"), str):
            result[value["id"]] = package.name
    return result


def build_preview(data_root: Path, release_root: Path | None = None) -> dict[str, object]:
    tools: dict[str, dict[str, object]] = {}
    system_roots: list[str] = []
    package_names = discover_ids(release_root)
    roots = [data_root / "publictools", data_root / "tools"]
    for root in roots:
        if not root.is_dir():
            continue
        for tool_dir in root.iterdir():
            if not tool_dir.is_dir():
                continue
            tool_id = tool_dir.name
            relative_tool = tool_dir.relative_to(data_root).as_posix()
            if tool_id in SYSTEM_DATA_DIRS:
                system_roots.append(relative_tool)
                continue
            item = tools.setdefault(
                tool_id,
                {
                    "id": tool_id,
                    "package": package_names.get(tool_id),
                    "current_roots": [],
                    "categories": {},
                    "target_root": f"tools/{tool_id}",
                },
            )
            item["current_roots"].append(relative_tool)
            categories = item["categories"]
            root_files = [path for path in tool_dir.iterdir() if path.is_file()]
            if root_files:
                categories.setdefault("config", {"files": 0, "bytes": 0, "sources": []})
                categories["config"]["files"] += len(root_files)
                categories["config"]["bytes"] += sum(path.stat().st_size for path in root_files)
                categories["config"]["sources"].append(relative_tool)
            for category in CATEGORIES:
                directory = tool_dir / category
                files, bytes_count = size_and_files(directory)
                if files:
                    categories.setdefault(category, {"files": 0, "bytes": 0, "sources": []})
                    categories[category]["files"] += files
                    categories[category]["bytes"] += bytes_count
                    categories[category]["sources"].append(directory.relative_to(data_root).as_posix())

    ordered = [tools[key] for key in sorted(tools)]
    return {
        "schema_version": 1,
        "mode": "read-only-preview",
        "data_root": str(data_root),
        "target_layout": "tools/<tool-id>/<category>",
        "categories": list(CATEGORIES),
        "system_roots": sorted(system_roots),
        "tools": ordered,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description="预览工具可变数据迁移，不移动文件")
    parser.add_argument("--data-root", required=True, type=Path)
    parser.add_argument("--release-root", type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    report = build_preview(args.data_root.resolve(), args.release_root.resolve() if args.release_root else None)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"迁移预览已生成：工具 {len(report['tools'])} 个，输出 {args.output}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
