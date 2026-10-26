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
        command = request["command"]
        # Preserve Unicode and native nonzero exit codes without bash/$ escaping.
        script = "$ErrorActionPreference='Stop'; $global:LASTEXITCODE=0; "
        script += "[Console]::OutputEncoding=[Text.UTF8Encoding]::new($false); $OutputEncoding=[Console]::OutputEncoding; "
        script += "try { & {\n" + command + "\n}; exit $LASTEXITCODE } catch { [Console]::Error.WriteLine($_.Exception.Message); exit 1 }"
        encoded = base64.b64encode(script.encode("utf-16le")).decode("ascii")
        flags = getattr(subprocess, "CREATE_NO_WINDOW", 0) | getattr(subprocess, "CREATE_NEW_PROCESS_GROUP", 0)
        with (directory / "output.log").open("wb") as output:
            process = subprocess.Popen([request["shell"], "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-EncodedCommand", encoded], cwd=request["cwd"], stdin=subprocess.DEVNULL, stdout=output, stderr=subprocess.STDOUT, creationflags=flags)
            (directory / "running.json").write_text(json.dumps({"pid": process.pid, "helper_pid": os.getpid(), "started": time.time()}), encoding="utf8")
            while process.poll() is None:
                if (directory / "cancel").exists():
                    subprocess.run(["taskkill.exe", "/PID", str(process.pid), "/T", "/F"], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, creationflags=flags, timeout=5)
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
