"""Local-only diagnostic program used to verify Agent file repair and lifecycle.
No account data, model calls, WeChat operations or external network access.
"""
import argparse
import json
import os
from pathlib import Path
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=18089)
    parser.add_argument("--run-id", default="local-probe")
    parser.add_argument("--state-file", type=Path)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    port = args.port
    if not 1 <= port <= 65535:
        parser.error("port out of range")
    if args.check:
        print(json.dumps({"valid": True, "host": "127.0.0.1", "port": port}))
        return
    state = {"run_id": args.run_id, "pid": os.getpid(), "platform": sys.platform,
             "script": str(Path(__file__).resolve()), "port": port, "events": []}
    lock = threading.Lock()

    def record(event):
        with lock:
            state["events"].append({"event": event, "time": time.time()})
            if args.state_file:
                args.state_file.parent.mkdir(parents=True, exist_ok=True)
                args.state_file.write_text(json.dumps(state, ensure_ascii=False, indent=2), encoding="utf8")

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *unused):
            pass

        def do_GET(self):
            if self.path != "/health":
                self.send_error(404)
                return
            body = json.dumps({**state, "status": "running"}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_POST(self):
            if self.path != "/shutdown" or self.headers.get("X-Probe-Run") != args.run_id:
                self.send_error(403)
                return
            record("shutdown_requested")
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b'{"stopping":true}')
            threading.Thread(target=self.server.shutdown, daemon=True).start()

    server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    record("started")
    try:
        server.serve_forever(poll_interval=0.1)
    finally:
        server.server_close()
        record("stopped")


if __name__ == "__main__":
    main()
