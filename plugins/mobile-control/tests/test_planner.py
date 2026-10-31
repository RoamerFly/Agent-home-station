"""Offline contract tests: no device, model, filesystem data, or network."""
import importlib.util
import io
import json
from pathlib import Path
import sys
import unittest


sys.dont_write_bytecode = True
ROOT = Path(__file__).parents[1]
planner_spec = importlib.util.spec_from_file_location("mobile_planner", ROOT / "runtime" / "planner.py")
planner = importlib.util.module_from_spec(planner_spec)
planner_spec.loader.exec_module(planner)
main_spec = importlib.util.spec_from_file_location("mobile_main", ROOT / "runtime" / "main.py")
main = importlib.util.module_from_spec(main_spec)
main_spec.loader.exec_module(main)


def arguments(**changes):
    value = {
        "task_id":"task-synthetic", "observation_id":"obs-synthetic", "source_version":"a" * 64,
        "task":{"goal":"打开已授权页面", "device_id":"phone-a", "recordings":[], "recent_executed_steps":[{"action":"wait"}] * 5},
        "observation":{"current_app":"com.example.home", "allowed_packages":["com.example.home", "com.browser"], "browsers":["com.browser"], "web_hosts":["example.test"], "width":1080, "height":2400, "ui_nodes":[{"text":"synthetic"}]},
        "allowed_actions":["open_url", "tap", "capture"],
    }
    value.update(changes)
    return value


def call(step):
    return json.dumps({"action":"tool", "tool":"device_step", "arguments":step}, ensure_ascii=False)


class PlannerTests(unittest.TestCase):
    def test_plans_source_bound_valid_step_and_preserves_recent_window(self):
        calls = []
        def host(params):
            calls.append(params)
            if params["operation"] == "describe_screen":
                return {"text":"普通页面"}
            return {"text":call({"action":"open_url", "summary":"打开授权页面", "arguments":{"package":"com.browser", "url":"https://example.test/path"}})}
        result = planner.plan(arguments(), host)
        self.assertEqual(result["source_version"], "a" * 64)
        self.assertEqual(result["step"]["action"], "open_url")
        self.assertEqual(calls[0], {"operation":"describe_screen", "observation_id":"obs-synthetic", "source_version":"a" * 64, "prompt":planner.VISUAL_PROMPT})
        self.assertEqual(calls[1]["operation"], "plan_step")
        self.assertIn("不要登录", calls[1]["system"])
        self.assertEqual(len(json.loads(calls[1]["turns"][0]["content"])["recent_executed_steps"]), 4)
        self.assertEqual(json.loads(calls[1]["tools"][0]["schema"])["required"], ["action"])

    def test_invalid_plan_gets_one_repair_and_cannot_escalate_actions(self):
        calls = []
        answers = [
            {"text":call({"action":"launch", "arguments":{"package":"com.browser"}})},
            {"text":call({"action":"wait", "summary":"等待"})},
        ]
        def host(params):
            calls.append(params)
            return {"text":"普通页面"} if params["operation"] == "describe_screen" else answers.pop(0)
        result = planner.plan(arguments(allowed_actions=[]), host)
        self.assertEqual(result["step"]["action"], "wait")
        self.assertEqual(len(calls), 3)
        self.assertEqual(calls[2]["turns"][-1]["content"], planner.REPAIR_PROMPT)

    def test_host_rejected_step_shapes_get_one_repair_then_wait(self):
        invalid_steps = [
            {"action":"tap", "arguments":{"x":1.5,"y":2}},
            {"action":"tap", "arguments":{"x":1080,"y":2}},
            {"action":"wait", "arguments":{"unexpected":True}},
            {"action":"open_url", "arguments":{"package":"com.browser","url":"https://other.test/path"}},
        ]
        for invalid in invalid_steps:
            with self.subTest(invalid=invalid):
                calls = []
                answers = [{"text":call(invalid)}, {"text":call({"action":"wait", "summary":"等待"})}]
                def host(params):
                    calls.append(params)
                    return {"text":"普通页面"} if params["operation"] == "describe_screen" else answers.pop(0)
                result = planner.plan(arguments(), host)
                self.assertEqual(result["step"]["action"], "wait")
                self.assertEqual([item["operation"] for item in calls], ["describe_screen", "plan_step", "plan_step"])

    def test_strict_json_rejects_duplicate_keys_constants_and_surrogates(self):
        for raw in ('{"a":1,"a":2}', '{"a":NaN}', '{"a":Infinity}', '{"a":"\\ud800"}'):
            with self.subTest(raw=raw), self.assertRaisesRegex(ValueError, "invalid_json"):
                planner.strict_loads(raw)
        with self.assertRaisesRegex(ValueError, "invalid_json"):
            main.Wire(io.StringIO('{"jsonrpc":"2.0","id":"a","id":"b"}\n'), io.StringIO()).read()

    def test_rejects_paths_png_and_provider_error_without_retry(self):
        for field in ("png", "account", "path"):
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, "device_planner_arguments_invalid"):
                planner.validate_arguments(arguments(**{field:"private"}))
        private_recordings = arguments(task={"goal":"x", "device_id":"phone-a", "recordings":[{"path":"C:/private"}], "recent_executed_steps":[]})
        with self.assertRaisesRegex(ValueError, "device_task_invalid"):
            planner.validate_arguments(private_recordings)
        calls = []
        def denied(params):
            calls.append(params)
            raise ValueError("host_service_denied_or_failed")
        with self.assertRaisesRegex(ValueError, "host_service_denied_or_failed"):
            planner.plan(arguments(), denied)
        self.assertEqual(len(calls), 1)

    def test_stdio_protocol_uses_only_model_request_and_returns_code_only(self):
        context = {"account":"a", "thread":"t", "requester":"u", "purpose":"p", "request_key":"k"}
        requests = [
            {"jsonrpc":"2.0", "id":"init", "method":"initialize", "params":{"_meta":{}}},
            {"jsonrpc":"2.0", "id":"call", "method":"tools/call", "params":{"name":"device.plan", "arguments":arguments(), "_meta":{"wechat/context":context}}},
            {"jsonrpc":"2.0", "id":"mobile-model-1", "result":{"text":"普通页面"}},
            {"jsonrpc":"2.0", "id":"mobile-model-2", "result":{"text":call({"action":"wait", "summary":"等待"})}},
        ]
        outgoing = io.StringIO()
        main.Wire(io.StringIO("".join(json.dumps(item, ensure_ascii=False) + "\n" for item in requests)), outgoing).serve()
        replies = [json.loads(line) for line in outgoing.getvalue().splitlines()]
        self.assertEqual(replies[0]["result"]["serverInfo"]["version"], "0.3.0")
        self.assertEqual(replies[1]["method"], "wechat/model_request")
        self.assertEqual(replies[1]["params"]["operation"], "describe_screen")
        self.assertEqual(replies[2]["params"]["operation"], "plan_step")
        self.assertEqual(replies[3]["result"]["step"]["action"], "wait")


if __name__ == "__main__":
    unittest.main()
