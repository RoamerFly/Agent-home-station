"""Compatibility entrypoint for the official speech package GPT-SoVITS runner.

No inference implementation is copied here. Managed invocations execute the
hash-indexed package runner directly; this source-tree CLI stays compatible.
"""
import importlib.util
import json
from pathlib import Path

_runner = Path(__file__).resolve().parents[1] / 'plugins/speech/runtime/gpt_sovits.py'
if not _runner.is_file():
    raise RuntimeError('official_speech_package_runner_required')
_spec = importlib.util.spec_from_file_location('official_speech_gpt_sovits', _runner)
_module = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_module)
parser = _module.parser
prepare = _module.prepare
synthesize = _module.synthesize
install_windows_audio_loader = _module.install_windows_audio_loader

if __name__ == '__main__':
    print(json.dumps(synthesize(parser().parse_args()), ensure_ascii=False))
