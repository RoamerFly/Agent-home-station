"""Replay synthetic native tool rounds against a source checkout using a Go overlay."""

import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-root", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--verify-current", action="store_true",
                        help="require supported scenarios and a completed diagnostic for each tool result")
    args = parser.parse_args()
    root = args.source_root.resolve()
    fixture = Path(__file__).resolve().parent / "testdata" / "tool_rounds_probe.go"
    virtual = root / "internal" / "admin" / "tool_rounds_overlay_probe_test.go"
    if virtual.exists() or not virtual.parent.is_dir():
        parser.error("expected an admin package with an unused overlay filename")
    commit = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
    dirty = bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=root))
    with tempfile.TemporaryDirectory() as temporary:
        directory = Path(temporary)
        overlay = directory / "overlay.json"
        overlay.write_text(json.dumps({"Replace": {str(virtual): str(fixture)}}), encoding="utf-8")
        report = directory / "result.json"
        subprocess.run(["go", "test", "-overlay", str(overlay), "./internal/admin",
                        "-run", "^TestSyntheticNativeToolRounds$", "-count=1", "-timeout", "3m"],
                       cwd=root, check=True, env=dict(os.environ, AHS_TOOL_ROUNDS_RESULT=str(report)))
        cases = json.loads(report.read_text(encoding="utf-8"))
    data = dict(fixture_version=1, source_commit=commit, source_dirty=dirty, provider_calls=0,
                wechat_sends=0, provider_usage=None, cases=cases)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    for case in cases:
        print(case["scenario"], case["outcome"], case["model_requests"], case["total_estimated_input"])
    if args.verify_current:
        for case in cases:
            expected = [name + "=tool_completed" for name in case["successful_tool_feedback"]]
            if case["outcome"] != "passed" or case["observed_terminal_tools"] != expected:
                print("Incomplete scenario or terminal diagnostics:", case["scenario"])
                return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
