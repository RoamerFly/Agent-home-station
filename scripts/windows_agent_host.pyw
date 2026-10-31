"""Private file transport for WSL -> Windows execution without a console.
Only launched by the owner-authorized host tool; never an HTTP server.
"""
import base64
import json
import os
from pathlib import Path
import subprocess
import sys
import time

# Embedded Python's isolated ._pth intentionally omits arbitrary script paths.
sys.path.insert(0, str(Path(__file__).resolve().parent))


def run(request_path):
    path = Path(request_path)
    directory = path.parent
    result = {"exit_code": -1}
    try:
        request = json.loads(path.read_text(encoding="utf8"))
        if request.get("sandbox") is not True: raise RuntimeError("sandbox_required: unrestricted Windows execution is disabled")
        direct = request.get("argv")
        if direct is not None and (not isinstance(direct, list) or not direct or any(not isinstance(x, str) or "\0" in x for x in direct) or not Path(direct[0]).is_absolute()):
            raise ValueError("invalid_isolated_argv")
        command = request.get("command", "")
        policy = request.get("music_policy", "")
        if policy and (policy != "registry-read-private-network-v1" or direct is None or command):
            raise ValueError("invalid_music_runtime_policy")
        if request.get("environment") and not policy:
            raise ValueError("music_runtime_environment_requires_authorized_policy")
        # Preserve Unicode and native nonzero exit codes without bash/$ escaping.
        script = "$ErrorActionPreference='Stop'; $global:LASTEXITCODE=0; "
        script += "[Console]::OutputEncoding=[Text.UTF8Encoding]::new($false); $OutputEncoding=[Console]::OutputEncoding; "
        script += "try { & {\n" + command + "\n}; exit $LASTEXITCODE } catch { [Console]::Error.WriteLine($_.Exception.Message); exit 1 }"
        encoded = base64.b64encode(script.encode("utf-16le")).decode("ascii")
        flags = getattr(subprocess, "CREATE_NO_WINDOW", 0) | getattr(subprocess, "CREATE_NEW_PROCESS_GROUP", 0)
        with (directory / "output.log").open("wb") as output, (directory / "stderr.log").open("wb") as diagnostic:
            from windows_lpac import launch
            if direct is not None:
                argv = direct
            elif Path(request["shell"]).name.lower() == "cmd.exe":
                argv=[request["shell"],"/d","/s","/c",command]
            else:
                argv=[request["shell"],"-NoProfile","-NonInteractive","-WindowStyle","Hidden","-EncodedCommand",encoded]
            process = launch(argv, request["cwd"], request["roots"], request.get("read_only", []), output, diagnostic if direct is not None else None,
                             music_runtime=bool(policy), environment=request.get("environment"))
            (directory / "running.json").write_text(json.dumps({"pid": process.pid, "helper_pid": os.getpid(), "started": time.time()}), encoding="utf8")
            while process.poll() is None:
                if (directory / "cancel").exists():
                    process.kill()
                    process.wait(timeout=5)
                    result["cancelled"] = True
                    break
                time.sleep(.1)
            result["exit_code"] = process.returncode
    except Exception as error:
        # Only a local diagnostic. Command/output files stay in the private spool.
        result["error"] = type(error).__name__ + ": " + str(error)
    result["finished"] = time.time()
    pending = directory / "result.pending"
    pending.write_text(json.dumps(result), encoding="utf8")
    pending.replace(directory / "result.json")


if __name__ == "__main__":
    run(sys.argv[1])
