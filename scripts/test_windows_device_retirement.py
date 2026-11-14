"""Synthetic dependency graphs for the Windows build retirement boundary."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
POWERSHELL = shutil.which("powershell") or shutil.which("pwsh")


@unittest.skipUnless(os.name == "nt" and POWERSHELL and shutil.which("go"), "Windows Go/PowerShell build guard")
class WindowsDeviceRetirementBoundary(unittest.TestCase):
    def setUp(self):
        self.scratch = tempfile.TemporaryDirectory(prefix="synthetic-retirement-")
        self.addCleanup(self.scratch.cleanup)
        self.root = Path(self.scratch.name)
        self.write("go.mod", "module wechat-bot\n\ngo 1.24\n")
        self.write("internal/adbserver/server.go", "package adbserver\n")
        self.write("internal/devicebroker/broker.go", 'package devicebroker\nimport _ "wechat-bot/internal/adbserver"\n')
        self.write("internal/admin/mobile_device_plugin.go", 'package admin\nimport _ "wechat-bot/internal/devicebroker"\n')
        self.write("cmd/wechat-admin/main.go", 'package main\nimport _ "wechat-bot/internal/admin"\nfunc main() {}\n')
        self.write("cmd/agent-home-station/main.go", "package main\nfunc main() {}\n")

    def write(self, name, text):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")

    def run_guard(self, target="Management"):
        return subprocess.run(
            [POWERSHELL, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File",
             str(ROOT / "scripts/Test-WindowsDeviceRetirement.ps1"),
             "-SourceDirectory", str(self.root), "-Target", target],
            capture_output=True, text=True, timeout=45,
            creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
        )

    def test_reviewed_legacy_bridge_and_empty_station_are_accepted(self):
        for target in ("Management", "Station"):
            result = self.run_guard(target)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_new_admin_bridge_file_is_rejected(self):
        self.write("internal/admin/new_driver.go", 'package admin\nimport _ "wechat-bot/internal/devicebroker"\n')
        result = self.run_guard()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Unreviewed Windows admin device bridge source", result.stderr)

    def test_command_cannot_bind_driver_directly(self):
        self.write("cmd/wechat-admin/driver.go", 'package main\nimport _ "wechat-bot/internal/devicebroker"\n')
        result = self.run_guard()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Unreviewed Windows device driver binding", result.stderr)

    def test_station_cannot_import_legacy_admin_indirectly(self):
        self.write("cmd/agent-home-station/legacy.go", 'package main\nimport _ "wechat-bot/internal/admin"\n')
        result = self.run_guard("Station")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("LocalStation imports a retired management or device runtime package", result.stderr)

    def test_even_reviewed_admin_source_cannot_bind_adb_directly(self):
        self.write("internal/admin/mobile_device_plugin.go", 'package admin\nimport _ "wechat-bot/internal/adbserver"\n')
        result = self.run_guard()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Unreviewed Windows device driver binding", result.stderr)


if __name__ == "__main__":
    unittest.main()
