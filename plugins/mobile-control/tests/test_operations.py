"""Offline device-facade contracts: no bridge, device, network, or model."""
import importlib.util
import io
import json
from pathlib import Path
import unittest


ROOT = Path(__file__).parents[1]
spec = importlib.util.spec_from_file_location("mobile_operations", ROOT / "runtime" / "operations.py")
operations = importlib.util.module_from_spec(spec)
spec.loader.exec_module(operations)
main_spec = importlib.util.spec_from_file_location("mobile_operations_main", ROOT / "runtime" / "main.py")
main = importlib.util.module_from_spec(main_spec)
main_spec.loader.exec_module(main)
VERSION = "b" * 64
OBS = "obs-" + "a" * 32
ARTIFACT = "clip-" + "c" * 32


class OperationsTests(unittest.TestCase):
    def test_each_operation_preserves_exact_source_and_original_request(self):
        cases = [
            ("device.list", {}),
            ("device.observe", {"device_id":"phone_a"}),
            ("device.act", {"device_id":"phone_a", "observation_id":OBS, "request_key":"key_a", "action":"tap", "x":1, "y":2}),
            ("device.record", {"device_id":"phone_a", "observation_id":OBS, "request_key":"key_a", "action":"capture", "seconds":4}),
            ("device.read_artifact", {"device_id":"phone_a", "artifact_id":ARTIFACT, "request_key":"key_a", "offset":0}),
        ]
        for operation, request in cases:
            with self.subTest(operation=operation):
                source, parsed = operations.validate_request(operation, {"source_version":VERSION,"request":request})
                self.assertEqual(source, VERSION)
                self.assertEqual(parsed, request)

    def test_rejects_raw_authority_overrides_and_unknown_fields(self):
        for field in ("serial", "path", "argv", "env", "account", "data_base64"):
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, "device_request_invalid"):
                operations.validate_request("device.observe", {"source_version":VERSION,"request":{"device_id":"phone_a",field:"forbidden"}})
        with self.assertRaisesRegex(ValueError,"device_operation_arguments_invalid"):
            operations.validate_request("device.list", {"source_version":VERSION,"request":{},"account":"private"})
        for source in ("", "invalid", "a" * 63):
            with self.assertRaisesRegex(ValueError,"device_source_version_invalid"):
                operations.validate_request("device.list", {"source_version":source,"request":{}})

    def test_text_url_and_action_request_are_never_rewritten(self):
        for action, extra in (("text", {"text":"original text"}), ("open_url", {"package":"com.browser","url":"https://example.test/?a=1&b=2"}), ("back", {})):
            request={"device_id":"phone_a","observation_id":OBS,"request_key":"original-key","action":action,**extra}
            _, parsed=operations.validate_request("device.act", {"source_version":VERSION,"request":request})
            self.assertEqual(parsed, request)

    def test_no_bridge_or_network_imports_and_ready_capabilities_match_permissions(self):
        source = (ROOT / "runtime" / "operations.py").read_text(encoding="utf8")
        for forbidden in ("import subprocess", "import socket", "import urllib", "bridge.py", "open("):
            self.assertNotIn(forbidden, source)
        caps = json.loads((ROOT / "capabilities.json").read_text(encoding="utf8"))["capabilities"]
        device_caps = [item for item in caps if item["id"] != "device.plan"]
        self.assertEqual({item["id"] for item in device_caps}, {"device.list", "device.observe", "device.act", "device.record", "device.read_artifact"})
        self.assertTrue(all(item["implementation"] == "ready" and item["discoverable"] and item["permissions"] == ["device"] for item in device_caps))
        manifest = json.loads((ROOT / "plugin.json").read_text(encoding="utf8"))
        self.assertNotIn("network", manifest["permissions"])

    def test_duplicate_json_frame_is_rejected_before_any_device_callback(self):
        wire = main.Wire(io.StringIO('{"jsonrpc":"2.0","id":"one","id":"two"}\n'), io.StringIO())
        with self.assertRaisesRegex(ValueError, "invalid_json"):
            wire.read()

    def test_tool_list_exposes_all_ready_capabilities_with_real_schemas(self):
        names = [tool["name"] for tool in main.TOOLS]
        self.assertEqual(set(names), {"device.plan", "device.list", "device.observe", "device.act", "device.record", "device.read_artifact"})
        for tool in main.TOOLS:
            self.assertIn("properties", tool["inputSchema"])
            self.assertFalse(tool["inputSchema"] == {"type":"object"})
        operations_schema = {tool["name"]: tool["inputSchema"] for tool in main.TOOLS}["device.observe"]
        self.assertEqual(operations_schema["required"], ["source_version", "request"])
        self.assertEqual(operations_schema["properties"]["request"]["required"], ["device_id"])


if __name__ == "__main__":
    unittest.main()
