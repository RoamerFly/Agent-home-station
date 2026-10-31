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


def invoke(operation, request, reply=None):
    calls = []
    def host(params):
        calls.append(params)
        return reply if reply is not None else {"operation":operation, "source_version":VERSION, "receipt":{"state":"accepted", "ui_nodes":[{"text":"untrusted device data"}]}}
    return operations.invoke(operation, {"source_version":VERSION, "request":request}, host), calls


class OperationsTests(unittest.TestCase):
    def test_each_operation_forwards_one_exact_source_bound_request(self):
        cases = [
            ("device.list", {}),
            ("device.observe", {"device_id":"phone_a"}),
            ("device.act", {"device_id":"phone_a", "observation_id":OBS, "request_key":"key_a", "action":"tap", "x":1, "y":2}),
            ("device.record", {"device_id":"phone_a", "observation_id":OBS, "request_key":"key_a", "action":"capture", "seconds":4}),
            ("device.read_artifact", {"device_id":"phone_a", "artifact_id":ARTIFACT, "request_key":"key_a", "offset":0}),
        ]
        for operation, request in cases:
            with self.subTest(operation=operation):
                result, calls = invoke(operation, request)
                self.assertEqual(calls, [{"operation":operation, "source_version":VERSION, "request":request}])
                self.assertEqual(result, {"operation":operation, "source_version":VERSION, "receipt":{"state":"accepted", "ui_nodes":[{"text":"untrusted device data"}]}})

    def test_rejects_tampering_unknown_fields_and_raw_bytes(self):
        with self.assertRaisesRegex(ValueError, "device_request_invalid"):
            invoke("device.observe", {"device_id":"phone_a", "serial":"private"})
        with self.assertRaisesRegex(ValueError, "device_operation_arguments_invalid"):
            operations.validate_request("device.list", {"source_version":VERSION, "request":{}, "account":"private"})
        request = {"device_id":"phone_a"}
        for reply in (
            {"operation":"device.observe", "source_version":"a" * 64, "receipt":{}},
            {"operation":"device.list", "source_version":VERSION, "receipt":{}},
            {"operation":"device.observe", "source_version":VERSION, "receipt":{"data_base64":"raw"}},
            {"operation":"device.observe", "source_version":VERSION, "receipt":{}, "extra":True},
        ):
            with self.subTest(reply=reply), self.assertRaisesRegex(ValueError, "device_host_receipt_invalid"):
                invoke("device.observe", request, reply)

    def test_safe_receipt_preserves_finite_time_and_coordinate_metadata(self):
        receipt = {"observed_at":1735689600.25, "created_at":1735689601.5, "width":1080, "height":2400, "ui_nodes":[{"bounds":[1,2,30,40]}]}
        result, _ = invoke("device.observe", {"device_id":"phone_a"}, {"operation":"device.observe", "source_version":VERSION, "receipt":receipt})
        self.assertEqual(result["receipt"], receipt)
        for bad in (float("nan"), float("inf"), float("-inf")):
            with self.subTest(bad=bad), self.assertRaisesRegex(ValueError, "device_host_receipt_invalid"):
                invoke("device.observe", {"device_id":"phone_a"}, {"operation":"device.observe", "source_version":VERSION, "receipt":{"observed_at":bad}})

    def test_request_key_and_unknown_outcome_are_not_rewritten(self):
        request = {"device_id":"phone_a", "observation_id":OBS, "request_key":"fixed_key", "action":"back"}
        result, calls = invoke("device.act", request, {"operation":"device.act", "source_version":VERSION, "receipt":{"state":"unknown", "reobserve_required":True}})
        self.assertEqual(calls[0]["request"]["request_key"], "fixed_key")
        self.assertEqual(result["receipt"]["state"], "unknown")

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
