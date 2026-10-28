"""Verified executable rollback after a confirmed web-service startup failure.

An expired health observation alone does not stop a live service. Credentials,
configuration, native WeChat and user data are never rollback targets.
"""
from pathlib import Path
import hashlib
import os
import tempfile
import time


def await_ready(probe, state, timeout=30, clock=time.monotonic, sleep=time.sleep):
    deadline = clock() + timeout
    while True:
        if probe():
            return "ready"
        if clock() >= deadline:
            current = state()
            # A failed process/auto-restart is evidence of an ended attempt;
            # a slow running process or unavailable status is not.
            if (current.get("ExecMainStatus", "0") != "0"
                    and current.get("ActiveState") in ("failed", "inactive", "activating")
                    and current.get("SubState") != "running"):
                return "startup_failed"
            return "observation_expired"
        sleep(0.5)


def verified_restore(binary, backup, expected_current, stop, start):
    binary, backup = Path(binary), Path(backup)
    folder = binary.parent
    if folder.resolve() != folder or binary.is_symlink() or backup.is_symlink():
        raise ValueError("Executable rollback requires canonical regular paths")
    if backup.parent != folder or binary.name != "wechat-admin":
        raise ValueError("Unexpected rollback target")
    previous = backup.read_bytes()
    digest = hashlib.sha256(previous).hexdigest()
    if previous[:4] != b"\x7fELF" or backup.name != "wechat-admin.previous-" + digest[:12]:
        raise ValueError("Rollback backup failed ELF/hash verification")
    current = binary.read_bytes()
    if hashlib.sha256(current).hexdigest() != expected_current:
        raise ValueError("Executable changed after update; rollback not performed")
    # Validate everything before interrupting the failed restart loop.
    failed = folder / ("wechat-admin.failed-" + expected_current[:12])
    if failed.exists():
        if failed.is_symlink() or hashlib.sha256(failed.read_bytes()).hexdigest() != expected_current:
            raise ValueError("Failed-executable evidence path conflicts")
    else:
        with failed.open("xb") as evidence:
            os.fchmod(evidence.fileno(), 0o700)
            evidence.write(current)
            evidence.flush()
            os.fsync(evidence.fileno())
    stop()
    # An external updater must not be overwritten during service shutdown.
    if hashlib.sha256(binary.read_bytes()).hexdigest() != expected_current:
        raise ValueError("Executable changed during stop; rollback not performed")
    pending = None
    try:
        with tempfile.NamedTemporaryFile(dir=folder, delete=False) as output:
            pending = Path(output.name)
            os.fchmod(output.fileno(), 0o700)
            output.write(previous)
            output.flush()
            os.fsync(output.fileno())
        pending.replace(binary)
    finally:
        if pending is not None and pending.exists():
            pending.unlink()
    start()


def recover_startup(binary, backup, expected_current, probe, state, stop, start,
                    timeout=30, clock=time.monotonic, sleep=time.sleep):
    result = await_ready(probe, state, timeout, clock, sleep)
    if result != "startup_failed":
        return result
    verified_restore(binary, backup, expected_current, stop, start)
    restored = await_ready(probe, state, timeout, clock, sleep)
    return "restored" if restored == "ready" else "restore_unverified"
