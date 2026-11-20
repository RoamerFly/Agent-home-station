#!/usr/bin/env python3
"""Report rebuildable release caches without deleting anything."""

from __future__ import annotations

import argparse
import json
from pathlib import Path


def classify(path: Path) -> str | None:
    parts = {part.lower() for part in path.parts}
    name = path.name.lower()
    if "__pycache__" in parts or name.endswith((".pyc", ".pyo")):
        return "python-cache"
    if "computecache" in parts or "torch-cache" in parts or "hf-cache" in parts:
        return "model-runtime-cache"
    if name.endswith((".tmp", ".part", ".download")):
        return "temporary-file"
    if "scratch" in parts or "validation" in parts:
        return "validation-or-scratch"
    if "logs" in parts or name.endswith((".log", ".out", ".err")):
        return "runtime-log"
    return None


def report(root: Path) -> dict[str, object]:
    groups: dict[str, dict[str, object]] = {}
    for path in root.rglob("*"):
        if not path.is_file():
            continue
        kind = classify(path)
        if kind is None:
            continue
        try:
            size = path.stat().st_size
        except OSError:
            continue
        item = groups.setdefault(kind, {"files": 0, "bytes": 0, "paths": []})
        item["files"] += 1
        item["bytes"] += size
        item["paths"].append({"path": path.relative_to(root).as_posix(), "bytes": size})
    return {"schema_version": 1, "mode": "read-only", "root": str(root), "groups": groups}


def main() -> int:
    parser = argparse.ArgumentParser(description="生成 release 可重建缓存清理报告")
    parser.add_argument("--root", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    result = report(args.root.resolve())
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"清理报告已生成：{len(result['groups'])} 类可重建文件")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
