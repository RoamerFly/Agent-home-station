"""Isolated music plugin. It has no direct network, media, or credential access."""
import importlib.util
import hashlib
import json
from pathlib import Path
import re
import sys

FRAME_LIMIT = 8 * 1024 * 1024
TOOL_SCHEMA = {
    "type": "object",
    "properties": {
        "prompt": {"type": "string", "maxLength": 2000},
        "lyrics": {"type": "string", "maxLength": 2000},
        "timeout_seconds": {"type": "integer", "minimum": 1},
    },
    "additionalProperties": False,
}
spec = importlib.util.spec_from_file_location("cloud_music", Path(__file__).with_name("cloud_music.py"))
cloud_music = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cloud_music)
local_spec = importlib.util.spec_from_file_location("local_music", Path(__file__).with_name("local_music.py"))
local_music = importlib.util.module_from_spec(local_spec)
local_spec.loader.exec_module(local_music)


def _duplicate_pairs(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("invalid_json")
        value[key] = item
    return value


def _no_surrogates(value):
    if isinstance(value, str):
        if any(0xD800 <= ord(char) <= 0xDFFF for char in value):
            raise ValueError("invalid_json")
    elif isinstance(value, list):
        for item in value:
            _no_surrogates(item)
    elif isinstance(value, dict):
        for key, item in value.items():
            _no_surrogates(key)
            _no_surrogates(item)


def strict_loads(raw):
    try:
        value = json.loads(raw, object_pairs_hook=_duplicate_pairs, parse_constant=lambda _: (_ for _ in ()).throw(ValueError("invalid_json")))
    except (TypeError, ValueError, json.JSONDecodeError):
        raise ValueError("invalid_json")
    _no_surrogates(value)
    if not isinstance(value, dict):
        raise ValueError("invalid_protocol")
    return value


class Wire:
    def __init__(self, incoming, outgoing):
        self.incoming, self.outgoing, self.sequence = incoming, outgoing, 0
        self.local_contexts = set()

    def read(self):
        line = self.incoming.readline(FRAME_LIMIT + 1)
        if not line:
            raise EOFError()
        if not line.endswith("\n") or len(line.encode("utf8")) > FRAME_LIMIT:
            raise ValueError("frame_limit")
        return strict_loads(line)

    def write(self, value):
        raw = json.dumps(value, ensure_ascii=False, separators=(",", ":"))
        if len(raw.encode("utf8")) + 1 > FRAME_LIMIT:
            raise ValueError("frame_limit")
        self.outgoing.write(raw + "\n")
        self.outgoing.flush()

    def host(self, method, params):
        self.sequence += 1
        identity = "music-model-" + str(self.sequence)
        self.write({"jsonrpc": "2.0", "id": identity, "method": method, "params": params})
        reply = self.read()
        if reply.get("jsonrpc") != "2.0" or reply.get("id") != identity:
            raise ValueError("host_response_identity_mismatch")
        if "error" in reply:
            raise ValueError("host_service_denied_or_failed")
        if not isinstance(reply.get("result"), dict):
            raise ValueError("invalid_host_response")
        return reply["result"]

    def local_request(self, context, arguments):
        identity = hashlib.sha256(json.dumps({key: context[key] for key in ("account", "thread", "requester", "purpose", "request_key")}, sort_keys=True, ensure_ascii=False).encode('utf8')).digest()
        if identity in self.local_contexts:
            raise ValueError('music_bridge_request_already_used')
        # Reserve before the host callback: an uncertain outcome is not a
        # reason to retry a POST or create another remote job.
        self.local_contexts.add(identity)
        return self.host('wechat/music_bridge', arguments)

    def serve(self):
        initialized, configuration = False, {}
        while True:
            try:
                request = self.read()
            except EOFError:
                return
            except ValueError:
                self.write({"jsonrpc": "2.0", "id": None, "error": {"code": -32600, "message": "invalid_request_or_page"}})
                continue
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
                    result = {"protocolVersion": "2025-06-18", "capabilities": {"tools": {}}, "serverInfo": {"name": "music", "version": "0.3.0"}}
                elif method == "ping" and initialized:
                    result = {}
                elif method == "tools/list" and initialized:
                    result = {"tools": [{"name": "music.generate", "description": "通过宿主绑定云端接口提交歌曲生成；不下载或发送音频。", "inputSchema": TOOL_SCHEMA}]}
                elif method == "tools/call" and initialized:
                    params = request.get("params")
                    context = params.get("_meta", {}).get("wechat/context", {}) if isinstance(params, dict) else {}
                    if not all(isinstance(context.get(k), str) and context[k] for k in ("account", "thread", "requester", "purpose", "request_key")):
                        raise ValueError("host_context_required")
                    if params.get("name") == "music.bridge_request":
                        result = local_music.bridge_request(params.get('arguments'), lambda value: self.local_request(context, value))
                    elif params.get("name") == "music.generate":
                        result = cloud_music.generate(params.get("arguments"), configuration, lambda value: self.host("wechat/model_request", value))
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
