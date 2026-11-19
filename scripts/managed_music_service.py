"""Per-account local/Linux music supervisor; inference uses an offline sandbox."""
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time
import urllib.request
import urllib.parse
import threading
import hashlib
import socket
from pathlib import PurePosixPath

sys.path.insert(0, str(Path(__file__).resolve().parent))

from managed_music_runner import command, launch_windows
from music_unix_client import open_request
from music_account_scope import HEADER as ACCOUNT_HEADER, account_scope
from managed_music_ipc import AccountIPC


def comfy_memory_arguments(mode):
    """Keep backend device selection automatic unless explicitly configured.

    Forced low VRAM also moves YuE's semantic model to CPU on the registered
    Comfy revision, which can dominate generation time.
    """
    flags = {'auto': [], 'normal': ['--normalvram'], 'low': ['--lowvram'], 'none': ['--novram']}
    if mode not in flags:
        raise ValueError('invalid_music_vram_mode')
    return list(flags[mode])


def external_comfy_url(value):
    """Validate a registered loopback Comfy endpoint.

    The Windows music supervisor may reuse a separately managed Station Comfy
    process.  Only an explicit loopback URL is accepted; the supervisor never
    turns this option into a general network proxy.
    """
    if not value:
        return None
    parsed = urllib.parse.urlsplit(value)
    if parsed.scheme != 'http' or parsed.hostname != '127.0.0.1' or parsed.username or parsed.password:
        raise ValueError('music_comfy_url_must_be_loopback')
    if parsed.path not in ('', '/') or parsed.query or parsed.fragment:
        raise ValueError('music_comfy_url_path_invalid')
    try:
        port = parsed.port
    except ValueError:
        raise ValueError('music_comfy_url_port_invalid') from None
    if port is None or not 1024 <= port <= 65535:
        raise ValueError('music_comfy_url_port_invalid')
    return f'http://127.0.0.1:{port}'


def yue_project_paths(public, controls):
    """Choose YuE paths for legacy workspaces and relocated tool packages.

    A portable package may keep its workflow files beside ``runtime`` while
    older registrations keep the app under ``projects/yue/app``.  Resolve
    only directories under the registered public root or this shipped control
    directory, never by searching the host filesystem.
    """
    public = Path(public)
    controls = Path(controls)
    project_candidates = (public / "projects/yue/app", public / "runtime", public)
    workflow_candidates = (controls / "workflows", public / "workflows", public / "runtime/workflows",
                           public / "runtime/workflows_api", public / "projects/yue/app/workflows_api")
    required = ("audio_yue2_text_to_music.json", "audio_yue2_music_cover.json")
    workflows = next((path for path in workflow_candidates
                      if path.is_dir() and all((path / name).is_file() for name in required)), None)
    if workflows is None:
        workflows = next((path for path in workflow_candidates if path.is_dir()), workflow_candidates[0])
    project = next((path for path in project_candidates
                    if (path / "comfy/ComfyUI/main.py").is_file()), None)
    if project is None:
        project = next((path for path in project_candidates if path.is_dir()), public)
    return project, workflows


def registered_comfy_command(config_file, url, state):
    """Start an explicitly registered backend as this music service's child."""
    from managed_music_runner import canonical
    config = json.loads(canonical(config_file).read_text(encoding="utf-8-sig"))
    if config.get("schema_version") != 1 or not config.get("enabled"):
        raise ValueError("music_comfy_registration_disabled")
    runtime = canonical(config["runtime"])
    def member(name):
        raw = config.get(name)
        if not isinstance(raw, str) or not raw or "\0" in raw:
            raise ValueError("music_comfy_registration_path_invalid")
        relative = PurePosixPath(raw.replace("\\", "/"))
        if relative.is_absolute() or ".." in relative.parts:
            raise ValueError("music_comfy_registration_path_invalid")
        path = canonical(runtime.joinpath(*relative.parts))
        if not path.is_relative_to(runtime):
            raise ValueError("music_comfy_registration_path_invalid")
        return path
    python, entry, frontend = member("python"), member("entry"), member("frontend")
    manifest_raw = config.get("manifest")
    if not isinstance(manifest_raw, str) or not manifest_raw or "\0" in manifest_raw:
        raise ValueError("music_comfy_manifest_invalid")
    manifest_member = PurePosixPath(manifest_raw.replace("\\", "/"))
    manifest_path = Path(manifest_raw)
    if not manifest_path.is_absolute():
        if manifest_member.is_absolute() or ".." in manifest_member.parts:
            raise ValueError("music_comfy_manifest_invalid")
        manifest_path = runtime.joinpath(*manifest_member.parts)
    manifest = json.loads(canonical(manifest_path).read_text(encoding="utf-8-sig"))
    if manifest.get("schema_version") != 1 or manifest.get("policy") not in ("station-yue-v1", "station-comfyui-v1"):
        raise ValueError("music_comfy_manifest_invalid")
    files = manifest.get("files", {})
    if not isinstance(files, dict):
        raise ValueError("music_comfy_manifest_invalid")
    for name, path in (("python",python),("entry",entry)):
        key = config[name].replace("\\", "/")
        expected = next((checksum for member, checksum in files.items()
                         if isinstance(member, str) and member.replace("\\", "/") == key), None)
        if not expected or hashlib.sha256(path.read_bytes()).hexdigest() != expected:
            raise ValueError("music_comfy_registration_digest_changed")
    port = urllib.parse.urlsplit(external_comfy_url(url)).port
    device = config.get("device")
    if device not in ("cpu","cuda"): raise ValueError("music_comfy_device_invalid")
    return [str(python), "-s", "-B", str(entry), "--listen", "127.0.0.1", "--port", str(port),
            "--base-directory", str(state/"base"), "--models-directory", str(canonical(config["models"])),
            "--input-directory", str(state/"input"), "--output-directory", str(state/"output"),
            "--user-directory", str(state/"user"), "--front-end-root", str(frontend),
            "--disable-all-custom-nodes", "--disable-api-nodes", "--disable-auto-launch", "--disable-metadata",
            *(["--cpu"] if device=="cpu" else ["--cuda-device",str(int(config.get("cuda_device",0)))])]

def main():
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--workspace', default=os.environ.get('WECHAT_WORKSPACE_ROOT', '/workspace'))
    parser.add_argument('--private', default=os.environ.get('MUSIC_PRIVATE_WORKSPACE'))
    parser.add_argument('--public-root', default=os.environ.get('MUSIC_PUBLIC_ROOT', ''),
                        help='registered public tool package; defaults to workspace/public_workspace')
    parser.add_argument('--singing-voice-config', default=os.environ.get('MUSIC_SINGING_VOICE_CONFIG', ''),
                        help='account-specific singing voice configuration outside the public package')
    parser.add_argument('--python', default='/usr/local/music-env/bin/python3.10')
    parser.add_argument('--token-file', default=os.environ.get('MUSIC_TOKEN_FILE', '/run/secrets/yue-token'))
    parser.add_argument('--ipc', default='/run/music')
    parser.add_argument('--bind', default='127.0.0.1')
    parser.add_argument('--port', type=int, default=6188)
    parser.add_argument('--startup-timeout', type=int, default=300)
    parser.add_argument('--vram-mode', choices=('auto', 'normal', 'low', 'none'),
                        default=os.environ.get('MUSIC_VRAM_MODE', 'auto'),
                        help='Comfy memory strategy. auto keeps backend device selection; low/none can put semantic generation on CPU.')
    parser.add_argument('--healthcheck', action='store_true')
    parser.add_argument('--comfy-url', default=os.environ.get('MUSIC_COMFY_URL'),
                        help='reuse a separately managed loopback ComfyUI endpoint')
    parser.add_argument('--comfy-input', default=os.environ.get('MUSIC_COMFY_INPUT'),
                        help='input directory shared with an external ComfyUI endpoint')
    parser.add_argument("--comfy-registration", help="owner-pinned backend registration; own the Comfy child lifecycle")
    parser.add_argument('--desktop', action='store_true', help='stop when the owning control pipe closes')
    args = parser.parse_args()
    memory_arguments = comfy_memory_arguments(args.vram_mode)
    comfy_url = external_comfy_url(args.comfy_url)
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
    public = Path(args.public_root).resolve(strict=True) if args.public_root else workspace / "public_workspace"
    from managed_music_runner import managed_public_root
    public = managed_public_root(public)
    if not args.private: raise ValueError("music_private_workspace_required")
    private = Path(args.private).resolve(strict=True)
    if private.parent != workspace / "pri_workspace":
        raise ValueError("music_account_directory_required")
    account_scope(private)
    state = private / "music"
    for name in ("input", "output", "user", "base", "jobs"):
        (state / name).mkdir(parents=True, exist_ok=True)
    python = str(Path(args.python).absolute())
    scripts = Path(__file__).resolve().parent
    inference_scripts = public / "projects/agent-tools"
    project, workflows = yue_project_paths(public, scripts)
    argv = [python, str(inference_scripts / "comfy_unix_entry.py"), "--socket", "/run/managed-music/comfy.sock",
            "--entry", str(project / "comfy/ComfyUI/main.py"), "--base-directory", str(state / "base"),
            "--models-directory", str(public / "models/yue"), "--input-directory", str(state / "input"),
            "--output-directory", str(state / "output"), "--user-directory", str(state / "user"),
            "--disable-all-custom-nodes", "--disable-api-nodes", "--disable-auto-launch", *memory_arguments]
    children = []
    stopping = threading.Event()
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
    if args.desktop:
        def control():
            for line in sys.stdin:
                if line.strip() == 'stop':
                    break
            stopping.set()
        threading.Thread(target=control, daemon=True).start()
    lease = None
    try:
        lease = AccountIPC(args.ipc, public, private)
        ipc, sock = lease.directory, lease.socket
        if args.comfy_registration:
            if not comfy_url: raise ValueError("music_comfy_registration_url_required")
            child_argv = registered_comfy_command(args.comfy_registration, comfy_url, state)
            # Never adopt an unrelated listener as this service's child.
            probe = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            try:
                if hasattr(socket,"SO_EXCLUSIVEADDRUSE"): probe.setsockopt(socket.SOL_SOCKET,socket.SO_EXCLUSIVEADDRUSE,1)
                probe.bind(("127.0.0.1",urllib.parse.urlsplit(comfy_url).port))
            finally: probe.close()
            children.append(subprocess.Popen(child_argv, cwd=str(Path(child_argv[3]).parent),
                creationflags=subprocess.CREATE_NO_WINDOW if sys.platform=="win32" else 0))
        if comfy_url is None:
            if sys.platform == "win32":
                argv[argv.index("--socket") + 1] = str(sock)
                children.append(launch_windows(public, private, private, argv, gpu=True))
                from windows_lpac import host_pipe_path
                sock = host_pipe_path(str(sock), children[0])
            else:
                children.append(subprocess.Popen(command(public, private, private, argv, gpu=True, ipc=ipc)))
            for _ in range(args.startup_timeout):
                if stopping.is_set():
                    return
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
        else:
            # A registered child belongs to this supervisor; an explicit URL
            # without registration retains the externally managed behavior.
            request = urllib.request.Request(comfy_url + '/system_stats')
            for _ in range(args.startup_timeout):
                if stopping.is_set():
                    return
                if children and children[0].poll() is not None:
                    raise RuntimeError("registered_comfy_exited")
                try:
                    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request, timeout=2) as reply:
                        json.load(reply)
                    break
                except (OSError, ValueError, urllib.error.HTTPError):
                    time.sleep(1)
            else:
                raise RuntimeError("external_comfy_start_timeout")
        bridge = [python, str(scripts / "yue_bridge.py"), "--project", str(project),
                  "--workflow-dir", str(workflows), "--state", str(state / "jobs"),
                  "--token-file", str(token), "--bind", args.bind, "--port", str(args.port),
                  "--managed-public", str(public), "--managed-private", str(private),
                  "--comfy-input", str(args.comfy_input or state / "input")]
        if comfy_url is None:
            bridge += ["--comfy-socket", str(sock)]
        else:
            bridge += ["--comfy-url", comfy_url]
        voice = Path(args.singing_voice_config).resolve(strict=True) if args.singing_voice_config else public / "manifests/singing-voice.config"
        if voice.is_file():
            bridge += ["--singing-voice-config", str(voice)]
        # The token-bearing HTTP bridge is trusted control code outside the
        # inference mount, using the same managed interpreter and clean startup.
        children.append(subprocess.Popen(bridge, creationflags=subprocess.CREATE_NO_WINDOW if sys.platform == "win32" else 0))
        while not stopping.is_set() and all(child.poll() is None for child in children):
            time.sleep(1)
        if stopping.is_set():
            return
        raise RuntimeError("music_child_exited")
    finally:
        try:
            stop()
        finally:
            if lease is not None:
                lease.close()


if __name__ == "__main__":
    main()
