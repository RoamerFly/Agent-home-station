"""Per-account Docker music supervisor; inference runs in an offline sandbox."""
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time
import urllib.request

from managed_music_runner import command
from music_unix_client import open_request
from music_account_scope import HEADER as ACCOUNT_HEADER, account_scope
from managed_music_ipc import AccountIPC


def main():
    token = Path(os.environ.get("MUSIC_TOKEN_FILE", "/run/secrets/yue-token"))
    if sys.argv[1:] == ["--healthcheck"]:
        request = urllib.request.Request("http://127.0.0.1:6188/health", headers={"Authorization": "Bearer " + token.read_text().strip(), ACCOUNT_HEADER: account_scope(os.environ["MUSIC_PRIVATE_WORKSPACE"])})
        with urllib.request.urlopen(request, timeout=4) as reply:
            if not json.load(reply).get("ready"):
                raise SystemExit(1)
        return
    public = Path("/workspace/public_workspace")
    private = Path(os.environ["MUSIC_PRIVATE_WORKSPACE"])
    if private.parent != Path("/workspace/pri_workspace") or len(private.name) != 64 or any(c not in "0123456789abcdef" for c in private.name):
        raise ValueError("music_account_directory_required")
    state = private / "music"
    for name in ("input", "output", "user", "base", "jobs"):
        (state / name).mkdir(parents=True, exist_ok=True)
    python = "/usr/local/music-env/bin/python3.10"
    scripts = Path("/usr/local/lib/wechat-music")
    project = public / "projects/yue/app"
    argv = [python, str(scripts / "comfy_unix_entry.py"), "--socket", "/run/managed-music/comfy.sock",
            "--entry", str(project / "comfy/ComfyUI/main.py"), "--base-directory", str(state / "base"),
            "--models-directory", str(public / "models/yue"), "--input-directory", str(state / "input"),
            "--output-directory", str(state / "output"), "--user-directory", str(state / "user"),
            "--disable-all-custom-nodes", "--disable-api-nodes", "--disable-auto-launch", "--lowvram"]
    children = []
    def stop(*unused):
        for child in reversed(children):
            if child.poll() is None:
                child.terminate()
        for child in reversed(children):
            try:
                child.wait(timeout=20)
            except subprocess.TimeoutExpired:
                child.kill(); child.wait()
    def interrupted(*unused):
        stop(); raise SystemExit(0)
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    lease = None
    try:
        lease = AccountIPC("/run/music", public, private)
        ipc, sock = lease.directory, lease.socket
        children.append(subprocess.Popen(command(public, private, private, argv, gpu=True, ipc=ipc)))
        for _ in range(300):
            if children[0].poll() is not None:
                raise RuntimeError("isolated_comfy_exited")
            try:
                with open_request(str(sock), "http://127.0.0.1:8188/system_stats", timeout=2) as reply:
                    json.load(reply)
                break
            except (OSError, ValueError):
                time.sleep(1)
        else:
            raise RuntimeError("isolated_comfy_start_timeout")
        bridge = [python, str(scripts / "yue_bridge.py"), "--project", str(project),
                  "--workflow-dir", str(scripts / "workflows"), "--state", str(state / "jobs"),
                  "--token-file", str(token), "--bind", "0.0.0.0", "--managed-public", str(public),
                  "--managed-private", str(private), "--comfy-socket", str(sock), "--comfy-input", str(state / "input")]
        voice = public / "manifests/singing-voice.config"
        if voice.is_file():
            bridge += ["--singing-voice-config", str(voice)]
        children.append(subprocess.Popen(bridge))
        while all(child.poll() is None for child in children):
            time.sleep(1)
        raise RuntimeError("music_child_exited")
    finally:
        try:
            stop()
        finally:
            if lease is not None:
                lease.close()


if __name__ == "__main__":
    main()
