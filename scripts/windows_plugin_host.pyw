"""Trusted LPAC stdio launcher. No network listener or unrestricted fallback.

The Go host supplies stdio handles and a private launch document. The plugin
inherits only these handles; the control document is outside its mounts.
"""
import ctypes
from ctypes import wintypes
import json
import msvcrt
import os
from pathlib import Path
import sys
import threading

sys.path.insert(0, str(Path(__file__).resolve().parent))


def stream(which, mode):
    kernel = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel.GetStdHandle.argtypes = [wintypes.DWORD]
    kernel.GetStdHandle.restype = wintypes.HANDLE
    handle = kernel.GetStdHandle(which & 0xffffffff)
    if not handle or handle == wintypes.HANDLE(-1).value:
        raise RuntimeError("plugin_stdio_handle_missing")
    flags = os.O_BINARY | (os.O_RDONLY if mode == "rb" else os.O_WRONLY)
    return os.fdopen(msvcrt.open_osfhandle(handle, flags), mode, buffering=0)


def main():
    # pythonw intentionally has no sys.stdin/sys.stdout objects. These are the
    # explicit pipe handles inherited from the host, never a console window.
    stdin, stdout, stderr = stream(-10, "rb"), stream(-11, "wb"), stream(-12, "wb")
    try:
        request = json.loads(Path(sys.argv[1]).read_text(encoding="utf8"))
        if set(request) != {"argv", "cwd", "read_only", "writable", "parent_pid"}:
            raise ValueError("invalid_plugin_launch_document")
        argv = request["argv"]
        if not isinstance(argv, list) or not argv or any(not isinstance(v, str) or "\0" in v for v in argv) or not Path(argv[0]).is_absolute():
            raise ValueError("invalid_plugin_argv")
        parent = request["parent_pid"]
        if type(parent) is not int or parent <= 0:
            raise ValueError("invalid_plugin_parent")
        kernel = ctypes.WinDLL("kernel32", use_last_error=True)
        kernel.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
        kernel.OpenProcess.restype = wintypes.HANDLE
        kernel.WaitForSingleObject.argtypes = [wintypes.HANDLE, wintypes.DWORD]
        kernel.WaitForSingleObject.restype = wintypes.DWORD
        handle = kernel.OpenProcess(0x00100000, False, parent)  # SYNCHRONIZE only
        if not handle or kernel.WaitForSingleObject(handle, 0) != 258:
            raise RuntimeError("plugin_parent_unavailable")
        def parent_exited():
            kernel.WaitForSingleObject(handle, 0xffffffff)
            # Closing this broker kills its LPAC Job and all plugin descendants.
            # No orphan can keep running with old credentials after host restart.
            os._exit(1)
        threading.Thread(target=parent_exited, daemon=True).start()
        from windows_lpac import launch
        process = launch(argv, request["cwd"], request["writable"], request["read_only"], stdout, stderr, input_stream=stdin)
        code = process.wait()
        return code if 0 <= code <= 255 else 1
    except Exception:
        # Do not serialize arbitrary exception strings containing local paths,
        # invocation text, environment values or plugin secrets.
        stderr.write(b"plugin_sandbox_launch_failed\n")
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
