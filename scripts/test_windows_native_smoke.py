"""Opt-in actual Windows binaries, isolated empty data, no login or send.

Set WECHAT_WINDOWS_NATIVE_PACKAGE to a locally built portable package with
DesktopTests.exe. The default source suite never launches account software.
"""
import json
import hashlib
import os
from pathlib import Path
import shutil
import socket
import subprocess
import time
import unittest

ROOT = Path(__file__).resolve().parents[1]


@unittest.skipUnless(os.name == "nt" and os.environ.get("WECHAT_WINDOWS_NATIVE_PACKAGE"), "opt-in Windows native package")
class WindowsNativeSmoke(unittest.TestCase):
    def test_real_controller_cycles_without_wechat_login(self):
        package = Path(os.environ["WECHAT_WINDOWS_NATIVE_PACKAGE"]).resolve(strict=True)
        scratch = ROOT / "temp" / "windows-native-smoke" / ("合成 空白实例 " + str(time.time_ns()))
        scratch.mkdir(parents=True)
        shutil.copytree(package / "runtimes", scratch / "runtimes")
        (scratch / "secrets").mkdir()
        reservations = [socket.socket(), socket.socket()]
        try:
            for listener in reservations:
                listener.bind(("127.0.0.1", 0))
            ports = [listener.getsockname()[1] for listener in reservations]
        finally:
            for listener in reservations:
                listener.close()
        config = {"backend": "windows-native", "data_root": "data", "account": "synthetic",
                  "admin_url": f"http://127.0.0.1:{ports[0]}", "native_url": f"http://127.0.0.1:{ports[1]}"}
        (scratch / "secrets/local-deployment.json").write_text(json.dumps(config), encoding="utf-8")
        result = subprocess.run([str(package / "DesktopTests.exe"), "--cycle", str(scratch)],
                                capture_output=True, encoding="utf-8", timeout=240,
                                creationflags=subprocess.CREATE_NO_WINDOW)
        (scratch / "controller-result.txt").write_text(result.stdout + result.stderr, encoding="utf-8")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("cycle passed twice", result.stdout)
        self.assertTrue((scratch / "data/admin/admin-token").is_file())
        self.assertTrue((scratch / "data/api-token").is_file())
        # Both actual processes must have relinquished their listening ports.
        for port in ports:
            with socket.socket() as connection:
                self.assertNotEqual(connection.connect_ex(("127.0.0.1", port)), 0)
        record = {"result": "passed", "cycles": 2, "real_login": False, "real_send": False,
                  "real_model_call": False, "ports_closed": True,
                  "package_manifest": json.loads((package / "runtimes/windows-x64/windows-native-manifest.json").read_text(encoding="utf-8"))}
        (scratch / "result.json").write_text(json.dumps(record, indent=2), encoding="utf-8")
        print("Native Windows lifecycle evidence:", scratch)

        # Inject a deliberately failing Web server only into this disposable
        # empty instance. Retry must not retain log locks from an exited child.
        admin = scratch / "runtimes/windows-x64/wechat-admin.exe"
        shutil.copyfile(package / "DesktopTests.exe", admin)
        manifest_path = scratch / "runtimes/windows-x64/windows-native-manifest.json"
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        manifest["admin_sha256"] = hashlib.sha256(admin.read_bytes()).hexdigest()
        manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
        failed = subprocess.run([str(package / "DesktopTests.exe"), "--failure-cycle", str(scratch)],
                                capture_output=True, encoding="utf-8", timeout=120,
                                creationflags=subprocess.CREATE_NO_WINDOW)
        (scratch / "failed-start-result.txt").write_text(failed.stdout + failed.stderr, encoding="utf-8")
        self.assertEqual(failed.returncode, 0, failed.stdout + failed.stderr)
        self.assertIn("recovery passed twice", failed.stdout)
        for port in ports:
            with socket.socket() as connection:
                self.assertNotEqual(connection.connect_ex(("127.0.0.1", port)), 0)


if __name__ == "__main__":
    unittest.main()
