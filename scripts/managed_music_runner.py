"""Trusted broker helper for an account's offline Linux music processes.

The HTTP/token-bearing broker stays outside this namespace. Only public code,
models/runtimes and ONE account's private workspace are mounted. This helper
does not add a host-shell fallback or pass broker credentials to inference.
"""
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys


def canonical(path):
    path = Path(path)
    if not path.is_absolute():
        raise ValueError("music_workspace_requires_absolute_path")
    for parent in [path, *path.parents]:
        if parent.is_symlink() or getattr(parent, "is_junction", lambda: False)():
            raise ValueError("music_workspace_link_not_allowed")
    return path.resolve(strict=True)


def command(public, private, cwd, argv, gpu=False, ipc=None):
    if sys.platform != "linux" or not shutil.which("bwrap"):
        raise RuntimeError("music_sandbox_unavailable")
    public, private, cwd = map(canonical, (public, private, cwd))
    if public.name != "public_workspace" or private.parent.name != "pri_workspace" or public.parent != private.parent.parent:
        raise ValueError("music_managed_layout_required")
    if not (cwd.is_relative_to(public) or cwd.is_relative_to(private)):
        raise ValueError("music_working_directory_outside_workspace")
    if not argv or any(not isinstance(x, str) or "\0" in x for x in argv):
        raise ValueError("music_argv_invalid")
    if not Path(argv[0]).is_absolute():
        raise ValueError("music_interpreter_requires_absolute_path")
    # Venv interpreters are normally symlinks into another managed runtime.
    # The target must still reside inside the mounted runtime or system /usr.
    binary = Path(argv[0]).resolve(strict=True)
    if not (binary.is_relative_to(public / "runtimes") or binary.is_relative_to(Path("/usr"))):
        raise ValueError("music_interpreter_not_managed")
    scratch = private / "scratch"
    scratch.mkdir(exist_ok=True)
    args = [shutil.which("bwrap"), "--die-with-parent", "--new-session", "--unshare-all", "--cap-drop", "ALL", "--clearenv"]
    for name in ("/usr", "/bin", "/sbin", "/lib", "/lib64"):
        path = Path(name)
        if path.is_symlink():
            args += ["--symlink", os.readlink(path), name]
        elif path.exists():
            args += ["--ro-bind", name, name]
    args += ["--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--tmpfs", "/dev/shm"]
    if Path("/etc/ld.so.cache").exists():
        args += ["--ro-bind", "/etc/ld.so.cache", "/etc/ld.so.cache"]
    if gpu:
        devices = [name for name in ("/dev/dxg", "/dev/nvidia0", "/dev/nvidiactl", "/dev/nvidia-uvm", "/dev/nvidia-uvm-tools") if Path(name).exists()]
        if not devices:
            raise RuntimeError("music_gpu_device_unavailable")
        for name in devices:
            args += ["--dev-bind", name, name]
    args += ["--ro-bind", str(public), str(public), "--bind", str(private), str(private)]
    if ipc is not None:
        # A dedicated, owner-only socket directory; never bind the broker's
        # state/token directory. DrvFS cannot host Unix sockets, so this narrow
        # IPC mount lives on the Linux filesystem instead.
        ipc = canonical(ipc)
        if ipc.stat().st_uid != os.getuid() or ipc.stat().st_mode & 0o077:
            raise ValueError("music_ipc_requires_private_directory")
        if any(not path.is_socket() for path in ipc.iterdir()):
            raise ValueError("music_ipc_contains_non_socket_files")
        args += ["--bind", str(ipc), "/run/managed-music"]
    env = {"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": str(scratch), "TMPDIR": "/tmp", "LANG": "C.UTF-8",
           "HF_HOME": str(scratch / "hf-cache"), "TORCH_HOME": str(scratch / "torch-cache"),
           "HF_HUB_OFFLINE": "1", "TRANSFORMERS_OFFLINE": "1", "PYTHONDONTWRITEBYTECODE": "1",
           "LD_LIBRARY_PATH": "/usr/lib/wsl/lib:/usr/lib/x86_64-linux-gnu"}
    for key, value in env.items():
        args += ["--setenv", key, value]
    return args + ["--chdir", str(cwd), "--", *argv]


def run(public, private, cwd, argv, *, gpu=False, timeout=None, ipc=None):
    child = subprocess.Popen(command(public, private, cwd, argv, gpu, ipc), env={"PATH": "/usr/bin:/bin"}, start_new_session=True)
    try:
        code = child.wait(timeout=timeout)
        if code:
            raise subprocess.CalledProcessError(code, argv)
    except BaseException:
        if child.poll() is None:
            os.killpg(child.pid, signal.SIGKILL)
            child.wait()
        raise


if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument("--public", required=True)
    parser.add_argument("--private", required=True)
    parser.add_argument("--cwd", required=True)
    parser.add_argument("--gpu", action="store_true")
    parser.add_argument("--ipc")
    parser.add_argument("argv", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    argv = args.argv[1:] if args.argv[:1] == ["--"] else args.argv
    run(args.public, args.private, args.cwd, argv, gpu=args.gpu, ipc=args.ipc)
