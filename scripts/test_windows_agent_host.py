"""Real Windows hidden process, Unicode, exit-code and cancellation checks."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
import ctypes
from ctypes import wintypes


@unittest.skipUnless(os.name == "nt", "requires native Windows PowerShell")
class HiddenWindowsTests(unittest.TestCase):
    def assert_no_window(self, pids):
        user32 = ctypes.windll.user32
        found = []
        callback_type = ctypes.WINFUNCTYPE(wintypes.BOOL, wintypes.HWND, wintypes.LPARAM)
        @callback_type
        def inspect(handle, unused):
            pid = wintypes.DWORD()
            user32.GetWindowThreadProcessId(handle, ctypes.byref(pid))
            if pid.value in pids and user32.IsWindowVisible(handle):
                found.append(pid.value)
            return True
        user32.EnumWindows(inspect, 0)
        self.assertEqual(found, [], "hidden helper exposed a visible window")

    def launch(self, directory, command):
        request = Path(directory) / "request.json"
        request.write_text(json.dumps({"cwd": directory, "command": command, "shell": str(Path(os.environ["SystemRoot"]) / "System32/WindowsPowerShell/v1.0/powershell.exe")}), encoding="utf8")
        pythonw = Path(sys.executable).with_name("pythonw.exe")
        self.assertTrue(pythonw.is_file())
        child = subprocess.Popen([str(pythonw), str(Path(__file__).with_name("windows_agent_host.pyw")), str(request)], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, creationflags=subprocess.CREATE_NO_WINDOW)
        self.addCleanup(lambda: child.kill() if child.poll() is None else None)
        return child

    def result(self, directory):
        path = Path(directory) / "result.json"
        for _ in range(150):
            if path.exists():
                return json.loads(path.read_text(encoding="utf8"))
            time.sleep(.1)
        self.fail("hidden command did not return a result")

    def test_unicode_quotes_and_real_native_exit_code(self):
        with tempfile.TemporaryDirectory() as directory:
            child = self.launch(directory, "[Console]::WriteLine('中文 $literal & 引号'); & '" + sys.executable + "' -c 'import sys;sys.exit(7)'")
            self.assertEqual(self.result(directory)["exit_code"], 7)
            self.assertEqual(child.wait(timeout=5), 0)
            self.assertIn("中文 $literal & 引号", (Path(directory) / "output.log").read_text(encoding="utf8"))

    def test_cancel_stops_owned_process(self):
        with tempfile.TemporaryDirectory() as directory:
            child = self.launch(directory, "Start-Sleep -Seconds 30; Write-Output should-not-complete")
            for _ in range(100):
                if (Path(directory) / "running.json").exists():
                    break
                time.sleep(.1)
            running = json.loads((Path(directory) / "running.json").read_text(encoding="utf8"))
            self.assert_no_window({running["pid"], running["helper_pid"]})
            (Path(directory) / "cancel").touch()
            result = self.result(directory)
            self.assertTrue(result["cancelled"])
            self.assertNotEqual(result["exit_code"], 0)
            child.wait(timeout=5)

    def test_native_child_also_inherits_no_console(self):
        with tempfile.TemporaryDirectory() as directory:
            command = "& '" + sys.executable + "' -c 'import ctypes;print(ctypes.windll.kernel32.GetConsoleWindow())'"
            child = self.launch(directory, command)
            self.assertEqual(self.result(directory)["exit_code"], 0)
            child.wait(timeout=5)
            text = (Path(directory) / "output.log").read_text(encoding="utf8").strip()
            self.assertEqual(int(text), 0, "native child unexpectedly acquired a console")


if __name__ == "__main__":
    unittest.main()
