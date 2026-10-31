"""Control-owned per-account Unix IPC lease, never mounted into inference."""
import errno
import os
from pathlib import Path
import socket
import stat
import sys
import uuid

from managed_music_runner import canonical
from music_account_scope import account_scope


def private_directory(path):
    canonical(path.parent)
    path.mkdir(mode=0o700, exist_ok=True)
    canonical(path)
    info = path.stat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
        raise ValueError("music_ipc_requires_private_directory")
    return path


def control_file(path):
    fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK, 0o600)
    info = os.fstat(fd)
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077 or info.st_nlink != 1:
        os.close(fd)
        raise ValueError("music_ipc_invalid_control_file")
    return fd


class AccountIPC:
    """Acquire before socket cleanup; release only after all children stop.

    The 96-bit directory prefix keeps Linux sun_path short. The full identity
    is separately persisted and checked, so a prefix collision fails closed.
    """
    def __init__(self, base, public, private):
        self.fd = None
        if sys.platform == "win32":
            self._windows(base, public, private)
            return
        if sys.platform != "linux":
            raise RuntimeError("music_sandbox_unavailable")
        import fcntl

        scope = account_scope(private)
        base = Path(base)
        if not base.is_absolute():
            raise ValueError("music_ipc_requires_absolute_path")
        # Control files must not be reachable through any Agent workspace.
        candidate = base.resolve()
        if candidate.is_relative_to(canonical(public)) or candidate.is_relative_to(canonical(private).parent):
            raise ValueError("music_ipc_control_inside_agent_workspace")
        private_directory(base)
        directory = private_directory(base / scope[:24])
        self.directory = directory / "s"
        self.socket = self.directory / "comfy.sock"
        if len(os.fsencode(self.socket)) > 107:
            raise ValueError("music_ipc_socket_path_too_long_choose_shorter_base")
        fd = control_file(directory / ".service.lock")
        try:
            try:
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                raise RuntimeError("music_account_service_already_running") from None
            self.fd = fd
            identity = control_file(directory / "account")
            try:
                saved = os.read(identity, 256)
                expected = (scope + "\n").encode("ascii")
                if saved and saved != expected:
                    raise ValueError("music_ipc_account_identity_mismatch")
                if not saved:
                    os.write(identity, expected)
                    os.fsync(identity)
            finally:
                os.close(identity)
            private_directory(self.directory)
            if any(p.name != "comfy.sock" for p in self.directory.iterdir()):
                raise ValueError("unexpected_music_ipc_file")
            self._remove_stale_socket()
        except BaseException:
            self.fd = None
            os.close(fd)
            raise

    def _windows(self, base, public, private):
        import msvcrt
        scope = account_scope(private)
        base = Path(base)
        if not base.is_absolute():
            raise ValueError("music_ipc_requires_absolute_path")
        candidate = base.resolve()
        if candidate.is_relative_to(canonical(public)) or candidate.is_relative_to(canonical(private).parent):
            raise ValueError("music_ipc_control_inside_agent_workspace")
        base.mkdir(mode=0o700, parents=True, exist_ok=True)
        canonical(base)
        directory = base / scope
        directory.mkdir(mode=0o700, exist_ok=True)
        canonical(directory)
        lock = directory / ".service.lock"
        if lock.is_symlink() or lock.is_junction():
            raise ValueError("music_ipc_invalid_control_file")
        fd = os.open(lock, os.O_RDWR | os.O_CREAT | os.O_BINARY, 0o600)
        try:
            if os.fstat(fd).st_nlink != 1:
                raise ValueError("music_ipc_invalid_control_file")
            if not os.fstat(fd).st_size:
                os.write(fd, b"0")
            os.lseek(fd, 0, os.SEEK_SET)
            try:
                msvcrt.locking(fd, msvcrt.LK_NBLCK, 1)
            except OSError:
                raise RuntimeError("music_account_service_already_running") from None
            identity = directory / "account"
            if identity.is_symlink() or identity.is_junction():
                raise ValueError("music_ipc_account_identity_mismatch")
            expected = scope + "\n"
            if identity.exists() and identity.read_text(encoding="utf8") != expected:
                raise ValueError("music_ipc_account_identity_mismatch")
            if not identity.exists():
                with identity.open("x", encoding="utf8") as output:
                    output.write(expected)
            self.fd, self.directory = fd, directory
            self.socket = "\\\\.\\pipe\\LOCAL\\WechatAgentMusic-" + scope[:24] + "-" + uuid.uuid4().hex
        except BaseException:
            os.close(fd)
            raise

    def _remove_stale_socket(self):
        try:
            info = self.socket.lstat()
        except FileNotFoundError:
            return
        if not stat.S_ISSOCK(info.st_mode) or info.st_uid != os.getuid():
            raise ValueError("unexpected_music_ipc_file")
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as probe:
            probe.settimeout(1)
            try:
                probe.connect(str(self.socket))
            except OSError as error:
                if error.errno not in (errno.ECONNREFUSED, errno.ENOENT):
                    raise
            else:
                raise RuntimeError("music_ipc_backend_still_running")
        try:
            current = self.socket.lstat()
        except FileNotFoundError:
            return
        if (current.st_dev, current.st_ino) != (info.st_dev, info.st_ino):
            raise RuntimeError("music_ipc_socket_changed")
        self.socket.unlink()

    def close(self):
        if self.fd is not None:
            os.close(self.fd)
            self.fd = None

    def __enter__(self):
        return self

    def __exit__(self, *unused):
        self.close()
