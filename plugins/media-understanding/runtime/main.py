"""Isolated media observation service. No direct network/files/credentials."""
import importlib.util
import json
from pathlib import Path
import re
import sys

spec = importlib.util.spec_from_file_location("observation", Path(__file__).with_name("observation.py"))
observation = importlib.util.module_from_spec(spec)
spec.loader.exec_module(observation)
FRAME_LIMIT = 8 * 1024 * 1024


class Wire:
    def __init__(self, incoming, outgoing):
        self.incoming, self.outgoing, self.sequence = incoming, outgoing, 0

    def read(self):
        line = self.incoming.readline(FRAME_LIMIT + 1)
        if not line:
            raise EOFError()
        if not line.endswith("\n") or len(line.encode("utf8")) > FRAME_LIMIT:
            raise ValueError("frame_limit")
        value = json.loads(line)
        if not isinstance(value, dict):
            raise ValueError("invalid_protocol")
        return value

    def write(self, value):
        raw = json.dumps(value, ensure_ascii=False, separators=(",", ":"))
        if len(raw.encode("utf8")) + 1 > FRAME_LIMIT:
            raise ValueError("frame_limit")
        self.outgoing.write(raw + "\n")
        self.outgoing.flush()

    def host(self, method, params):
        self.sequence += 1
        identity = "media-model-" + str(self.sequence)
        self.write({"jsonrpc": "2.0", "id": identity, "method": method, "params": params})
        reply = self.read()
        if reply.get("jsonrpc") != "2.0" or reply.get("id") != identity:
            raise ValueError("host_response_identity_mismatch")
        if "error" in reply:
            raise ValueError("host_service_denied_or_failed")
        if not isinstance(reply.get("result"), dict):
            raise ValueError("invalid_host_response")
        return reply["result"]

    def serve(self):
        initialized = False
        configuration = {}
        while True:
            try:
                request = self.read()
            except EOFError:
                return
            if "id" not in request:
                continue
            try:
                if request.get("jsonrpc") != "2.0":
                    raise ValueError("invalid_protocol")
                method = request.get("method")
                if method == "initialize":
                    configuration = request.get("params", {}).get("_meta", {}).get("wechat/configuration", {}).get("values", {})
                    if not isinstance(configuration, dict):
                        raise ValueError("invalid_configuration")
                    initialized = True
                    result = {"protocolVersion": "2025-06-18", "capabilities": {"tools": {}}, "serverInfo": {"name": "media-understanding", "version": "0.1.1"}}
                elif method == "ping" and initialized:
                    result = {}
                elif method == "tools/call" and initialized:
                    params = request["params"]
                    context = params.get("_meta", {}).get("wechat/context", {})
                    if not all(isinstance(context.get(k), str) and context[k] for k in ("account", "thread", "requester", "purpose", "request_key")):
                        raise ValueError("host_context_required")
                    if params.get("name") == "media.observe":
                        result = observation.observe(params.get("arguments"), configuration, lambda params: self.host("wechat/model_request", params))
                    else:
                        raise ValueError("capability_unavailable")
                else:
                    raise ValueError("method_unavailable")
            except (ValueError, KeyError, TypeError, LookupError, AttributeError, RecursionError) as error:
                code = str(error)
                result = {"ok": False, "error": code if re.fullmatch(r"[a-z_]{1,96}", code) else "invalid_request_or_page"}
            self.write({"jsonrpc": "2.0", "id": request["id"], "result": result})


if __name__ == "__main__":
    sys.stdin.reconfigure(encoding="utf8")
    sys.stdout.reconfigure(encoding="utf8")
    Wire(sys.stdin, sys.stdout).serve()
