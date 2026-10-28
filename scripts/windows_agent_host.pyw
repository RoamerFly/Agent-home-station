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


def run(request_path):
    path = Path(request_path)
    directory = path.parent
    result = {"exit_code": -1}
    try:
        request = json.loads(path.read_text(encoding="utf8"))
        if request.get("sandbox") is not True: raise RuntimeError("sandbox_required: unrestricted Windows execution is disabled")
        command = request["command"]
        # Preserve Unicode and native nonzero exit codes without bash/$ escaping.
        script = "$ErrorActionPreference='Stop'; $global:LASTEXITCODE=0; "
        script += "[Console]::OutputEncoding=[Text.UTF8Encoding]::new($false); $OutputEncoding=[Console]::OutputEncoding; "
        script += "try { & {\n" + command + "\n}; exit $LASTEXITCODE } catch { [Console]::Error.WriteLine($_.Exception.Message); exit 1 }"
        encoded = base64.b64encode(script.encode("utf-16le")).decode("ascii")
        flags = getattr(subprocess, "CREATE_NO_WINDOW", 0) | getattr(subprocess, "CREATE_NEW_PROCESS_GROUP", 0)
        with (directory / "output.log").open("wb") as output:
            from windows_lpac import launch
            if Path(request["shell"]).name.lower() == "cmd.exe":
                argv=[request["shell"],"/d","/s","/c",command]
            else:
                argv=[request["shell"],"-NoProfile","-NonInteractive","-WindowStyle","Hidden","-EncodedCommand",encoded]
            process = launch(argv, request["cwd"], request["roots"], request.get("read_only", []), output)
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
