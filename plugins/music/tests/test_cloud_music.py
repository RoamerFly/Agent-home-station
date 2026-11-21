"""The retired cloud entry must never appear in the active music runtime."""
import importlib.util
import io
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("music_main", Path(__file__).resolve().parents[1] / "runtime/main.py")
main = importlib.util.module_from_spec(spec)
spec.loader.exec_module(main)


class RetiredCloudMusicTests(unittest.TestCase):
    def test_runtime_lists_only_local_operations_and_rejects_cloud_call(self):
        context = {"account": "test", "thread": "test", "requester": "owner", "purpose": "test", "request_key": "one"}
        messages = [
            {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}},
            {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}},
            {"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "music.generate", "arguments": {"prompt": "test"}, "_meta": {"wechat/context": context}}},
        ]
        output = io.StringIO()
        main.Wire(io.StringIO("\n".join(json.dumps(item) for item in messages) + "\n"), output).serve()
        rows = [json.loads(line) for line in output.getvalue().splitlines()]
        self.assertEqual([tool["name"] for tool in rows[1]["result"]["tools"]], ["music.bridge_request", "music.service_control", "music.prepare"])
        self.assertEqual(rows[2]["result"]["error"], "capability_unavailable")
        self.assertNotIn("wechat/model_request", output.getvalue())


if __name__ == "__main__":
    unittest.main()
