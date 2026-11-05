"""Compatibility launcher for the media-understanding local ASR runtime.

The implementation lives in the plugin package. Installers must deploy that
package runtime alongside this launcher at the fixed relative location; this
file never accepts a caller-controlled module or filesystem path.
"""

import runpy
from pathlib import Path


def plugin_entry():
    repository_entry = Path(__file__).resolve().parents[1] / "plugins" / "media-understanding" / "runtime" / "local_asr.py"
    installed_entry = Path(__file__).resolve().parents[2] / "plugins" / "media-understanding" / "runtime" / "local_asr.py"
    for entry in (repository_entry, installed_entry):
        if entry.is_file():
            return entry
    raise RuntimeError("media_understanding_local_asr_runtime_missing")


if __name__ == "__main__":
    runpy.run_path(str(plugin_entry()), run_name="__main__")
