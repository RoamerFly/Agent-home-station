"""Compatibility entry point; maintained updater lives in scripts/."""
from pathlib import Path
import runpy

if __name__ == "__main__":
    runpy.run_path(str(Path(__file__).resolve().parents[2] / "scripts/update_local_web.py"), run_name="__main__")
