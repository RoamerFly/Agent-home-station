"""Compile or explicitly run the preserved developer-only voice auditions.

No arguments print help. --prepare clears both provider-live flags; --run-live
additionally requires the original selected audition flag to equal 1.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile


KINDS = {
    "clone": ("live_voice_clone_test.go", "TestLiveBailianReferenceVoiceVariants", "WECHAT_VOICE_CLONE_LIVE"),
    "design": ("live_voice_design_test.go", "TestLiveBailianWhaleVoiceCandidates", "WECHAT_VOICE_DESIGN_LIVE"),
}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    action = parser.add_mutually_exclusive_group()
    action.add_argument("--prepare", action="store_true", help="Compile the two preserved auditions with all provider-live flags removed")
    action.add_argument("--run-live", action="store_true", help="Run only the selected explicitly enabled audition; may incur provider charges")
    parser.add_argument("--kind", choices=["clone", "design", "all"], default="all")
    args = parser.parse_args(argv)
    if not args.prepare and not args.run_live:
        parser.print_help()
        return 0
    directory = Path(__file__).resolve().parent
    repository = directory.parents[4]
    if not (repository / "go.mod").is_file() or not (repository / "internal/admin").is_dir():
        parser.error("this tool requires the original repository admin test harness")
    selected = list(KINDS) if args.kind == "all" else [args.kind]
    environment = dict(os.environ)
    if args.run_live:
        if any(environment.get(KINDS[kind][2]) != "1" for kind in selected):
            parser.error("--run-live also requires every selected original WECHAT_VOICE_*_LIVE flag to equal 1")
    else:
        for _, _, flag in KINDS.values():
            environment.pop(flag, None)
    replacements = {}
    for filename, _, _ in KINDS.values():
        replacements[str(repository / "internal/admin" / filename)] = str(directory / (filename + ".in"))
    tests = "^(" + "|".join(KINDS[kind][1] for kind in selected) + ")$"
    # Only this generated private directory is removed by TemporaryDirectory.
    with tempfile.TemporaryDirectory(prefix="wechat-legacy-audition-") as temporary:
        generated = Path(temporary).resolve()
        if generated.parent != Path(tempfile.gettempdir()).resolve():
            raise RuntimeError("unexpected temporary overlay directory")
        overlay = generated / "overlay.json"
        overlay.write_text(json.dumps({"Replace": replacements}), encoding="utf8")
        return subprocess.run(["go", "test", "-overlay", str(overlay), "./internal/admin", "-run", tests, "-count=1", "-v"], cwd=repository, env=environment, check=False).returncode


if __name__ == "__main__":
    raise SystemExit(main())
