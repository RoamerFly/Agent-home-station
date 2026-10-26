"""Synthetic local lifecycle checks; no WeChat or provider access."""
import json
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time
import unittest
import urllib.error
import urllib.request

SCRIPT = Path(__file__).with_name("agent_probe_service.py")


class ProbeTests(unittest.TestCase):
    def test_check_and_invalid_port(self):
        result = subprocess.run([sys.executable, str(SCRIPT), "--check"], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["host"], "127.0.0.1")
        bad = subprocess.run([sys.executable, str(SCRIPT), "--port", "-1", "--check"], capture_output=True, text=True)
        self.assertNotEqual(bad.returncode, 0)
        self.assertIn("port out of range", bad.stderr)

    def test_actual_process_health_and_shutdown(self):
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        client = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory) / "state.json"
            process = subprocess.Popen([sys.executable, str(SCRIPT), "--port", str(port), "--run-id", "synthetic-probe", "--state-file", str(state)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0))
            try:
                url = f"http://127.0.0.1:{port}"
                for _ in range(50):
                    try:
                        with client.open(url + "/health", timeout=1) as response:
                            health = json.load(response)
                        break
                    except OSError:
                        time.sleep(0.1)
                else:
                    self.fail("probe never became healthy")
                self.assertEqual(health["pid"], process.pid)
                self.assertEqual(health["platform"], sys.platform)
                with self.assertRaises(urllib.error.HTTPError) as denied:
                    client.open(urllib.request.Request(url + "/shutdown", data=b"", method="POST"), timeout=2)
                self.assertEqual(denied.exception.code, 403)
                request = urllib.request.Request(url + "/shutdown", data=b"", method="POST", headers={"X-Probe-Run": "synthetic-probe"})
                with client.open(request, timeout=2) as response:
                    self.assertTrue(json.load(response)["stopping"])
                self.assertEqual(process.wait(timeout=5), 0)
                events = json.loads(state.read_text(encoding="utf8"))["events"]
                self.assertEqual([x["event"] for x in events], ["started", "shutdown_requested", "stopped"])
                with self.assertRaises(OSError):
                    client.open(url + "/health", timeout=1)
            finally:
                if process.poll() is None:
                    process.terminate()
                    process.wait(timeout=5)


if __name__ == "__main__":
    unittest.main()
