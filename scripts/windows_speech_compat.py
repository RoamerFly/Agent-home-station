"""Keep the bundled synchronous speech worker compatible with offline LPAC."""

import importlib.machinery
import os
from pathlib import Path
import sys
import tempfile
from types import ModuleType


def _deny_proactor(*_args, **_kwargs):
    raise PermissionError("Windows socket I/O is disabled for local speech")


class _OfflineOverlappedLoader:
    def __init__(self, original):
        self.original = original
        self.denied = False

    def create_module(self, spec):
        try:
            return self.original.create_module(spec)
        except PermissionError as error:
            if error.winerror != 10013:
                raise
            self.denied = True
            return ModuleType(spec.name)

    def exec_module(self, module):
        if not self.denied:
            try:
                self.original.exec_module(module)
                return
            except PermissionError as error:
                if error.winerror != 10013:
                    raise
        module.CreateIoCompletionPort = _deny_proactor


class _OfflineOverlappedFinder:
    def find_spec(self, fullname, path=None, target=None):
        # asyncio imports this optional Windows transport through
        # unittest.mock during Torch initialization. Inferencing is synchronous.
        if fullname == "_overlapped":
            spec = importlib.machinery.PathFinder.find_spec(fullname, path, target)
            if spec is not None:
                spec.loader = _OfflineOverlappedLoader(spec.loader)
            return spec
        return None


def initialize():
    if os.name != "nt" or os.environ.get("WECHAT_WINDOWS_SPEECH_RUNTIME") != "1":
        return
    sys.meta_path.insert(0, _OfflineOverlappedFinder())
    # Use the private working directory instead of Windows' redirected, deeply
    # nested AppContainer temporary path for a disposable diagnostic sink.
    task = Path.cwd()
    os.environ.update(TMP=str(task), TEMP=str(task), TMPDIR=str(task))
    tempfile.tempdir = str(task)
    sink = task / f"null-{os.getpid()}.tmp"
    sink.write_bytes(b"")
    os.devnull = str(sink)
