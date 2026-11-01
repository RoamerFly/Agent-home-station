"""Offline lifecycle wire contract; no subprocess, network or model calls."""
import copy
import importlib.util
import io
import json
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("music_service_main", ROOT / "runtime/main.py")
main = importlib.util.module_from_spec(spec)
spec.loader.exec_module(main)


class ServiceControlTests(unittest.TestCase):
    def wire(self, arguments, repeat=False, denied=False):
        context = {key: "synthetic" for key in ("account", "thread", "requester", "purpose", "request_key")}
        initialize = {"jsonrpc": "2.0", "id": 1, "method": "initialize"}
        call = {"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {
            "name": "music.service_control", "arguments": arguments, "_meta": {"wechat/context": context}}}
        host = {"jsonrpc": "2.0", "id": "music-model-1"}
        if denied:
            host["error"] = {"code": -32001, "message": "denied"}
        else:
            host["result"] = {"running": False, "coverage": "owned_supervisor_only"}
        rows = [initialize, call, host]
        if repeat:
            again = copy.deepcopy(call)
            again["id"] = 3
            rows.append(again)
        output = io.StringIO()
        main.Wire(io.StringIO("\n".join(json.dumps(row) for row in rows) + "\n"), output).serve()
        return [json.loads(row) for row in output.getvalue().splitlines()]

    def test_only_fixed_actions_cross_host_callback_once(self):
        for action in ("start", "status", "stop"):
            for denied in (False, True):
                with self.subTest(action=action, denied=denied):
                    rows = self.wire({"action": action}, repeat=True, denied=denied)
                    callbacks = [row for row in rows if row.get("method") == "wechat/service_control"]
                    self.assertEqual(len(callbacks), 1)
                    self.assertEqual(callbacks[0]["params"], {"action": action})
                    self.assertEqual(rows[-1]["result"]["error"], "music_service_request_already_used")

    def test_arguments_cannot_supply_host_launch_authority(self):
        for arguments in ({"action": "shell"}, {"action": "start", "argv": ["unsafe"]},
                          {"action": "stop", "pid": 1}, {"action": "start", "token": "unsafe"}, {}, None):
            with self.subTest(arguments=arguments):
                rows = self.wire(arguments)
                self.assertFalse(any(row.get("method") == "wechat/service_control" for row in rows))
                self.assertEqual(rows[1]["result"]["error"], "invalid_service_control")


if __name__ == "__main__":
    unittest.main()
