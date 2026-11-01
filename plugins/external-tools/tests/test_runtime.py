import importlib.util
import io
import json
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location("external_tools", Path(__file__).parents[1] / "runtime" / "main.py")
runtime = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runtime)


CATALOGUE = {"version": 1, "tools": [
    {"id": "speech", "kind": "http", "description": "Synthetic host-bound request.", "input_schema": {"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"], "additionalProperties": False}, "route": "speech_api", "max_bytes": 4096, "timeout_seconds": 30},
    {"id": "remote", "kind": "mcp", "description": "Parsed but not exposed before a host proxy exists.", "input_schema": {"type": "object", "properties": {}, "additionalProperties": False}, "mcp_server": "remote_server"},
]}


class ExternalToolsRuntimeTests(unittest.TestCase):
    def test_projects_only_http_tools_and_emits_host_only_intent(self):
        catalogue = runtime.parse_declarations(CATALOGUE)
        self.assertEqual([x["id"] for x in catalogue["http"]], ["speech"])
        self.assertEqual([x["id"] for x in catalogue["mcp"]], ["remote"])
        calls = []
        result = runtime.execute(catalogue, "external_http_speech", {"text": "synthetic"}, lambda intent: calls.append(intent) or {"result": {"text": "accepted"}})
        self.assertEqual(result, {"text": "accepted"})
        self.assertEqual(calls, [{"route": "speech_api", "body": {"text": "synthetic"}, "max_bytes": 4096, "timeout_seconds": 30}])
        self.assertNotIn("endpoint", json.dumps(calls))

    def test_rejects_dynamic_destinations_and_does_not_call_host(self):
        for mutated in ({**CATALOGUE, "tools": [{**CATALOGUE["tools"][0], "route": "https://example.test"}]}, {"version": 1, "tools": [{"id": "bad", "kind": "http", "description": "x", "input_schema": {"type": "object"}, "route": "ok", "endpoint": "https://example.test"}]}):
            with self.assertRaises(ValueError):
                runtime.parse_declarations(mutated)
        called = False
        def host(_):
            nonlocal called
            called = True
            return {}
        with self.assertRaises(ValueError):
            runtime.execute(runtime.parse_declarations(CATALOGUE), "external_http_speech", [], host)
        self.assertFalse(called)

    def test_stdio_protocol_requires_catalogue_and_context(self):
        incoming = io.StringIO("\n".join(json.dumps(x) for x in [
            {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"_meta": {"wechat/host_metadata": {"external_tools": CATALOGUE}}}},
            {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}},
            {"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "external_http_speech", "arguments": {"text": "x"}, "_meta": {"wechat/context": {}}}},
        ]) + "\n")
        outgoing = io.StringIO()
        runtime.Wire(incoming, outgoing).serve()
        replies = [json.loads(line) for line in outgoing.getvalue().splitlines()]
        self.assertEqual(replies[1]["result"]["tools"][0]["name"], "external_http_speech")
        self.assertEqual(replies[2]["result"]["error"], "host_context_required")

    def test_stdio_uses_route_intent_not_a_network_destination(self):
        context = {"account": "a", "thread": "t", "requester": "r", "purpose": "p", "request_key": "k"}
        incoming = io.StringIO("\n".join(json.dumps(x) for x in [
            {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"_meta": {"wechat/host_metadata": {"external_tools": CATALOGUE}}}},
            {"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "external_http_speech", "arguments": {"text": "x"}, "_meta": {"wechat/context": context}}},
            {"jsonrpc": "2.0", "id": "external-http-1", "result": {"result": {"text": "accepted"}}},
        ]) + "\n")
        outgoing = io.StringIO()
        runtime.Wire(incoming, outgoing).serve()
        replies = [json.loads(line) for line in outgoing.getvalue().splitlines()]
        intent = replies[1]
        self.assertEqual(intent["method"], "wechat/http_route")
        self.assertEqual(intent["params"], {"route": "speech_api", "body": {"text": "x"}, "max_bytes": 4096, "timeout_seconds": 30})
        self.assertNotIn("endpoint", json.dumps(intent))
        self.assertEqual(replies[2]["result"], {"text": "accepted"})


if __name__ == "__main__":
    unittest.main()
