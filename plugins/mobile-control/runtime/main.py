"""Isolated MCP entry point for side-effect-free mobile task planning."""
import importlib.util
import json
from pathlib import Path
import re
import sys


_SPEC = importlib.util.spec_from_file_location("mobile_control_planner", Path(__file__).with_name("planner.py"))
planner = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(planner)
_OPERATIONS_SPEC = importlib.util.spec_from_file_location("mobile_control_operations", Path(__file__).with_name("operations.py"))
operations = importlib.util.module_from_spec(_OPERATIONS_SPEC)
_OPERATIONS_SPEC.loader.exec_module(operations)
FRAME_LIMIT = 8 * 1024 * 1024
PLAN_SCHEMA = {"type":"object", "properties":{"task_id":{"type":"string"}, "observation_id":{"type":"string"}, "source_version":{"type":"string","pattern":"^[0-9a-f]{64}$"}, "task":{"type":"object"}, "observation":{"type":"object"}, "allowed_actions":{"type":"array","items":{"type":"string"}}}, "required":["task_id","observation_id","source_version","task","observation","allowed_actions"], "additionalProperties":False}
TOOLS = [{"name":"device.plan", "description":"Plan one validated next step from a host-bound mobile observation. This does not observe, operate, record, or send anything.", "inputSchema":PLAN_SCHEMA}] + [{"name":operation, "description":"Host-mediated registered-device operation. Source binding and authorization are checked by the host.", "inputSchema":operations.tool_schema(operation)} for operation in sorted(operations.OPERATIONS)]


class Wire:
    def __init__(self, incoming, outgoing):
        self.incoming, self.outgoing, self.sequence = incoming, outgoing, 0

    def read(self):
        line = self.incoming.readline(FRAME_LIMIT + 1)
        if not line:
            raise EOFError()
        if not line.endswith("\n") or len(line.encode("utf8")) > FRAME_LIMIT:
            raise ValueError("frame_limit")
        value = planner.strict_loads(line)
        if not isinstance(value, dict):
            raise ValueError("invalid_protocol")
        return value

    def write(self, value):
        raw = json.dumps(value, ensure_ascii=False, separators=(",", ":"))
        if len(raw.encode("utf8")) + 1 > FRAME_LIMIT:
            raise ValueError("frame_limit")
        self.outgoing.write(raw + "\n")
        self.outgoing.flush()

    def host(self, params):
        self.sequence += 1
        identity = "mobile-model-" + str(self.sequence)
        self.write({"jsonrpc":"2.0", "id":identity, "method":"wechat/model_request", "params":params})
        reply = self.read()
        if reply.get("jsonrpc") != "2.0" or reply.get("id") != identity:
            raise ValueError("host_response_identity_mismatch")
        if "error" in reply:
            raise ValueError("host_service_denied_or_failed")
        if not isinstance(reply.get("result"), dict):
            raise ValueError("invalid_host_response")
        return reply["result"]

    def device(self, params):
        self.sequence += 1
        identity = "mobile-device-" + str(self.sequence)
        self.write({"jsonrpc":"2.0", "id":identity, "method":"wechat/device_request", "params":params})
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
        while True:
            try:
                request = self.read()
            except EOFError:
                return
            except ValueError:
                # Malformed frames have no trustworthy request identity. Do
                # not echo raw input or terminate the isolated process.
                continue
            if "id" not in request:
                continue
            try:
                if request.get("jsonrpc") != "2.0":
                    raise ValueError("invalid_protocol")
                method = request.get("method")
                if method == "initialize":
                    metadata = request.get("params", {}).get("_meta", {})
                    if not isinstance(metadata, dict):
                        raise ValueError("invalid_protocol")
                    initialized = True
                    result = {"protocolVersion":"2025-06-18", "capabilities":{"tools":{}}, "serverInfo":{"name":"mobile-control", "version":"0.3.0"}}
                elif method == "ping" and initialized:
                    result = {}
                elif method == "tools/list" and initialized:
                    result = {"tools":TOOLS}
                elif method == "tools/call" and initialized:
                    params = request.get("params")
                    context = params.get("_meta", {}).get("wechat/context", {}) if isinstance(params, dict) else {}
                    if not all(isinstance(context.get(key), str) and context[key] for key in ("account", "thread", "requester", "purpose", "request_key")):
                        raise ValueError("host_context_required")
                    if params.get("name") != "device.plan":
                        if params.get("name") not in operations.OPERATIONS:
                            raise ValueError("capability_unavailable")
                        result = operations.invoke(params["name"], params.get("arguments"), self.device)
                    else:
                        result = planner.plan(params.get("arguments"), self.host)
                else:
                    raise ValueError("method_unavailable")
            except (ValueError, KeyError, TypeError, LookupError, AttributeError, RecursionError) as error:
                code = str(error)
                result = {"ok":False, "error":code if re.fullmatch(r"[a-z_]{1,96}", code) else "device_request_invalid"}
            self.write({"jsonrpc":"2.0", "id":request["id"], "result":result})


if __name__ == "__main__":
    sys.stdin.reconfigure(encoding="utf8")
    sys.stdout.reconfigure(encoding="utf8")
    Wire(sys.stdin, sys.stdout).serve()
