"""Per-account local/Linux music supervisor; inference uses an offline sandbox."""
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


def main():
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--workspace', default=os.environ.get('WECHAT_WORKSPACE_ROOT', '/workspace'))
    parser.add_argument('--private', default=os.environ.get('MUSIC_PRIVATE_WORKSPACE'))
    parser.add_argument('--python', default='/usr/local/music-env/bin/python3.10')
    parser.add_argument('--token-file', default=os.environ.get('MUSIC_TOKEN_FILE', '/run/secrets/yue-token'))
    parser.add_argument('--ipc', default='/run/music')
    parser.add_argument('--bind', default='127.0.0.1')
    parser.add_argument('--port', type=int, default=6188)
    parser.add_argument('--startup-timeout', type=int, default=300)
    parser.add_argument('--healthcheck', action='store_true')
    args = parser.parse_args()
    if args.startup_timeout <= 0: raise ValueError('startup_timeout_must_be_positive')
    if not 1 <= args.port <= 65535: raise ValueError('invalid_music_port')
    token = Path(args.token_file).resolve(strict=True)
    if args.healthcheck:
        if not args.private:raise ValueError("music_private_workspace_required")
        request = urllib.request.Request(f"http://127.0.0.1:{args.port}/health", headers={"Authorization": "Bearer " + token.read_text().strip(), ACCOUNT_HEADER: account_scope(args.private)})
        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request, timeout=4) as reply:
            if not json.load(reply).get("ready"):
                raise SystemExit(1)
        return
    workspace = Path(args.workspace).resolve(strict=True)
    public = workspace / "public_workspace"
    if not args.private: raise ValueError("music_private_workspace_required")
    private = Path(args.private).resolve(strict=True)
    if private.parent != workspace / "pri_workspace" or len(private.name) != 64 or any(c not in "0123456789abcdef" for c in private.name):
        raise ValueError("music_account_directory_required")
    state = private / "music"
    for name in ("input", "output", "user", "base", "jobs"):
        (state / name).mkdir(parents=True, exist_ok=True)
    ipc = Path(args.ipc)
    ipc.mkdir(mode=0o700, exist_ok=True)
    ipc.chmod(0o700)
    sock = ipc / "comfy.sock"
    if sock.exists():
        if not sock.is_socket():
            raise ValueError("unexpected_music_ipc_file")
        sock.unlink()
    python = str(Path(args.python).absolute())
    scripts = Path(__file__).resolve().parent
    inference_scripts = public / "projects/agent-tools"
    workflows = scripts / "workflows"
    if not workflows.is_dir(): workflows = scripts.parent / "components/yue2/app/workflows_api"
    project = public / "projects/yue/app"
    argv = [python, str(inference_scripts / "comfy_unix_entry.py"), "--socket", "/run/managed-music/comfy.sock",
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
    try:
        children.append(subprocess.Popen(command(public, private, private, argv, gpu=True, ipc=ipc)))
        for _ in range(args.startup_timeout):
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
                  "--workflow-dir", str(workflows), "--state", str(state / "jobs"),
                  "--token-file", str(token), "--bind", args.bind, "--port", str(args.port), "--managed-public", str(public),
                  "--managed-private", str(private), "--comfy-socket", str(sock), "--comfy-input", str(state / "input")]
        voice = public / "manifests/singing-voice.config"
        if voice.is_file():
            bridge += ["--singing-voice-config", str(voice)]
        children.append(subprocess.Popen(bridge))
        while all(child.poll() is None for child in children):
            time.sleep(1)
        raise RuntimeError("music_child_exited")
    finally:
        stop()


if __name__ == "__main__":
    main()
