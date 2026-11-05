import hashlib
from pathlib import Path
import tempfile
import unittest

from local_web_recovery import recover_startup, verified_restore


class Clock:
    def __init__(self):
        self.now = 0

    def __call__(self):
        return self.now

    def sleep(self, seconds):
        self.now += seconds


class RecoveryTests(unittest.TestCase):
    def fixture(self, root):
        binary = root / "wechat-admin"
        binary.write_bytes(b"\x7fELFnew")
        old = b"\x7fELFold"
        backup = root / ("wechat-admin.previous-" + hashlib.sha256(old).hexdigest()[:12])
        backup.write_bytes(old)
        return binary, backup, hashlib.sha256(binary.read_bytes()).hexdigest()

    def test_live_timeout_is_not_restart_authorization(self):
        clock = Clock()
        events = []
        result = recover_startup(None, None, None, lambda: False,
            lambda: {"ActiveState": "active", "SubState": "running", "ExecMainStatus": "0"},
            lambda: events.append("stop"), lambda: events.append("start"),
            timeout=1, clock=clock, sleep=clock.sleep)
        self.assertEqual(result, "observation_expired")
        self.assertEqual(events, [])

    def test_crash_restores_only_verified_program_and_preserves_evidence(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            binary, backup, current = self.fixture(root)
            config = root / "config.json"
            config.write_bytes(b"synthetic settings")
            events = []
            clock = Clock()
            result = recover_startup(binary, backup, current, lambda: events == ["stop", "start"],
                lambda: {"ActiveState": "activating", "SubState": "auto-restart", "ExecMainStatus": "1"},
                lambda: events.append("stop"), lambda: events.append("start"),
                timeout=1, clock=clock, sleep=clock.sleep)
            self.assertEqual(result, "restored")
            self.assertEqual(binary.read_bytes(), backup.read_bytes())
            self.assertEqual(config.read_bytes(), b"synthetic settings")
            self.assertEqual((root / ("wechat-admin.failed-" + current[:12])).read_bytes(), b"\x7fELFnew")

    def test_changed_backup_or_current_rejected_before_stop(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            binary, backup, current = self.fixture(root)
            events = []
            backup.write_bytes(b"\x7fELFtampered")
            with self.assertRaises(ValueError):
                verified_restore(binary, backup, current, lambda: events.append("stop"), lambda: None)
            self.assertEqual(events, [])
            binary, backup, current = self.fixture(root)
            binary.write_bytes(b"\x7fELFother update")
            with self.assertRaises(ValueError):
                verified_restore(binary, backup, current, lambda: events.append("stop"), lambda: None)
            self.assertEqual(events, [])

    def test_delayed_health_preserves_original_live_instance(self):
        clock = Clock()
        events = []
        result = recover_startup(None, None, None, lambda: clock.now >= .5, lambda: {},
            lambda: events.append("stop"), lambda: events.append("start"),
            timeout=1, clock=clock, sleep=clock.sleep)
        self.assertEqual(result, "ready")
        self.assertEqual(events, [])


if __name__ == "__main__":
    unittest.main()
