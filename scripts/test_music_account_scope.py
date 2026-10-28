import hashlib
from contextlib import ExitStack
import json
from pathlib import Path
import socket
import socketserver
import subprocess
import sys
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler

from music_account_scope import HEADER, account_scope
from managed_music_ipc import AccountIPC
from process_window import no_console_options


class MusicAccountScopeTests(unittest.TestCase):
    def test_requires_account_hash_directory(self):
        digest = hashlib.sha256(b"synthetic-account").hexdigest()
        self.assertEqual(account_scope(Path("/workspace/pri_workspace") / digest), digest)
        for path in ("/workspace/pri_workspace/name", "/workspace/public_workspace/" + digest,
                     "/workspace/pri_workspace/" + "A" * 64):
            with self.assertRaises(ValueError):
                account_scope(path)

    @unittest.skipUnless(sys.platform == "linux", "two local Linux broker processes with Unix backend")
    def test_two_concurrent_brokers_reject_wrong_account_even_with_same_token(self):
        class Backend(BaseHTTPRequestHandler):
            def do_GET(self):
                data = b'{}'
                self.send_response(200)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def log_message(self, *unused):
                pass

        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            public = root / "public_workspace"
            public.mkdir()
            workflows = public / "workflows"
            workflows.mkdir()
            for name in ("audio_yue2_music_cover.json", "audio_yue2_text_to_music.json"):
                (workflows / name).write_text('{}')
            token = root / "control-token"
            token.write_text("synthetic-local-broker-token")
            children, logs, services = [], [], []
            client = urllib.request.build_opener(urllib.request.ProxyHandler({}))

            def request(base, scope, route="/health", body=None, authenticated=True):
                headers = {HEADER: scope}
                if authenticated:
                    headers["Authorization"] = "Bearer " + token.read_text()
                data = None if body is None else json.dumps(body).encode()
                req = urllib.request.Request(base + route, data=data, headers=headers)
                try:
                    with client.open(req, timeout=2) as response:
                        return response.status, response.read(), response.headers
                except urllib.error.HTTPError as error:
                    return error.code, error.read(), error.headers

            with ExitStack() as stack:
                try:
                    for account in ("synthetic-one", "synthetic-two"):
                        scope = hashlib.sha256(account.encode()).hexdigest()
                        private = root / "pri_workspace" / scope
                        state = private / "music/jobs"
                        state.mkdir(parents=True)
                        lease = stack.enter_context(AccountIPC(root / "ipc", public, private))
                        sock = lease.socket
                        backend = stack.enter_context(socketserver.UnixStreamServer(str(sock), Backend))
                        worker = threading.Thread(target=backend.serve_forever, daemon=True)
                        worker.start()
                        stack.callback(worker.join)
                        stack.callback(backend.shutdown)
                        (state / "audio.wav").write_bytes(b"synthetic-audio:" + account.encode())
                        (state / "jobs.json").write_text(json.dumps({"same-job-id": {
                            "id": "same-job-id", "state": "completed", "file": "audio.wav",
                            "title": account,
                        }}))
                        with socket.socket() as probe:
                            probe.bind(("127.0.0.1", 0))
                            port = probe.getsockname()[1]
                        log = (root / (account + ".log")).open("wb")
                        logs.append(log)
                        argv = [sys.executable, str(Path(__file__).with_name("yue_bridge.py")),
                                "--project", str(public), "--workflow-dir", str(workflows),
                                "--state", str(state), "--token-file", str(token), "--port", str(port),
                                "--managed-public", str(public), "--managed-private", str(private),
                                "--comfy-socket", str(sock), "--comfy-input", str(private / "music/input")]
                        child = subprocess.Popen(argv, stdout=log, stderr=log, **no_console_options())
                        children.append(child)
                        base = "http://127.0.0.1:" + str(port)
                        deadline = time.monotonic() + 15
                        while True:
                            if child.poll() is not None:
                                self.fail("synthetic broker failed to start")
                            try:
                                code, data, _ = request(base, scope)
                                if code == 200 and json.loads(data).get("ready"):
                                    break
                            except (OSError, ValueError):
                                pass
                            if time.monotonic() >= deadline:
                                self.fail("synthetic broker startup timeout")
                            time.sleep(.05)
                        services.append((base, scope, state, account))

                    for index, (base, scope, state, account) in enumerate(services):
                        other = services[1 - index][1]
                        for route in ("/health", "/parameters", "/rvc-health",
                                      "/jobs/same-job-id", "/jobs/same-job-id/audio"):
                            for bad_scope in (other, ""):
                                code, data, _ = request(base, bad_scope, route)
                                self.assertEqual(code, 409, route)
                                self.assertEqual(json.loads(data), {"error": "music_account_scope_mismatch"})
                        for route in ("/jobs", "/rvc-jobs", "/jobs/"+'a'*32+"/resume"):
                            self.assertEqual(request(base, other, route, {})[0], 409)
                        code, data, headers = request(base, scope, "/jobs/same-job-id/audio")
                        self.assertEqual(code, 200)
                        self.assertEqual(data, b"synthetic-audio:" + account.encode())
                        self.assertEqual(headers.get(HEADER), scope)
                        code, _, headers = request(base, scope, authenticated=False)
                        self.assertEqual(code, 401)
                        self.assertIsNone(headers.get(HEADER))
                        self.assertEqual(len(json.loads((state / "jobs.json").read_text())), 1)
                finally:
                    for child in children:
                        child.terminate()
                    for child in children:
                        try:
                            child.wait(timeout=10)
                        except subprocess.TimeoutExpired:
                            child.kill()
                            child.wait()
                    for log in logs:
                        log.close()


if __name__ == "__main__":
    unittest.main()
