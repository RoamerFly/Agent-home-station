"""Synthetic contract tests for media.listen; no network or media files."""
import copy
import importlib.util
import io
import json
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("audio_observation", ROOT / "runtime" / "audio_observation.py")
audio_observation = importlib.util.module_from_spec(spec)
spec.loader.exec_module(audio_observation)

AUDIO = {"media_ref": "audio_1", "sha256": "a" * 64, "version": 2, "mime": "audio/wav"}
CONFIG = {"audio_review_model": "synthetic-audio-review", "max_tokens": 768}
REPLY = {"choices": [{"message": {"content": "合成音频内容观察"}, "finish_reason": "stop"}], "model": "provider-audio-model"}


class AudioObservationTests(unittest.TestCase):
    def setUp(self):
        self.calls = []

    def request(self, value):
        self.calls.append(value)
        return copy.deepcopy(REPLY)

    def test_host_route_body_is_reference_only_and_thinking_disabled(self):
        result = audio_observation.observe({"audio": AUDIO, "prompt": "概述可听到的内容", "max_tokens": 1200, "timeout_seconds": 42}, CONFIG, self.request)
        self.assertEqual(result["status"], "observed")
        self.assertEqual(result["sources"], [AUDIO])
        self.assertTrue(result["untrusted_external_content"])
        self.assertEqual(len(self.calls), 1)
        call = self.calls[0]
        self.assertEqual((call["route"], call["timeout_seconds"], call["max_bytes"]), ("audio-review", 42, 1024 * 1024))
        body = call["body"]
        self.assertEqual((body["model"], body["stream"], body["enable_thinking"], body["max_tokens"]), ("synthetic-audio-review", True, False, 1200))
        content = body["messages"]
        self.assertEqual(content, [{"role": "user", "content": [{"type": "text", "text": "概述可听到的内容"}, {"type": "input_audio", "input_audio": {"data": {"$media_ref": AUDIO}, "format": "wav"}}]}])
        self.assertNotIn("path", json.dumps(call))
        self.assertNotIn("base64", json.dumps(call))

    def test_length_is_partial_and_only_plain_content_is_accepted(self):
        reply = copy.deepcopy(REPLY)
        reply["choices"][0]["finish_reason"] = "length"
        self.assertEqual(audio_observation.observe({"audio": AUDIO, "prompt": "x"}, CONFIG, lambda _: reply)["status"], "partial")
        invalid = [
            {"choices": []},
            {"choices": [{"message": {"content": ""}, "finish_reason": "stop"}]},
            {"choices": [{"message": {"content": "x", "tool_calls": [{}]}, "finish_reason": "stop"}]},
            {"choices": [{"message": {"content": "x", "function_call": {}}, "finish_reason": "stop"}]},
            {"choices": [{"message": {"content": "x"}, "finish_reason": "content_filter"}]},
        ]
        for reply in invalid:
            with self.subTest(reply=reply), self.assertRaises(ValueError):
                audio_observation.observe({"audio": AUDIO, "prompt": "x"}, CONFIG, lambda _: reply)

    def test_invalid_args_or_config_never_reach_host(self):
        invalid = [
            {"audio": dict(AUDIO, mime="audio/mp3"), "prompt": "x"},
            {"audio": dict(AUDIO, media_ref="../../private"), "prompt": "x"},
            {"audio": dict(AUDIO, version=True), "prompt": "x"},
            {"audio": dict(AUDIO, path="C:/private.wav"), "prompt": "x"},
            {"audio": AUDIO, "prompt": " "},
            {"audio": AUDIO, "prompt": "x", "timeout_seconds": True},
            {"audio": AUDIO, "prompt": "x", "voice_id": "someone"},
        ]
        for args in invalid:
            with self.subTest(args=args), self.assertRaises(ValueError):
                audio_observation.observe(args, CONFIG, self.request)
        with self.assertRaisesRegex(ValueError, "audio_review_model_not_configured"):
            audio_observation.observe({"audio": AUDIO, "prompt": "x"}, {}, self.request)
        self.assertEqual(self.calls, [])

    def test_prompt_limit_counts_unicode_characters_not_utf8_bytes(self):
        accepted = "声" * 8192
        result = audio_observation.observe({"audio": AUDIO, "prompt": accepted}, CONFIG, self.request)
        self.assertEqual(result["status"], "observed")
        with self.assertRaisesRegex(ValueError, "invalid_audio_observation_prompt"):
            audio_observation.observe({"audio": AUDIO, "prompt": "声" * 8193}, CONFIG, self.request)
        self.assertEqual(len(self.calls), 1)

    def test_stdio_dispatch_uses_audio_review_only(self):
        main_spec = importlib.util.spec_from_file_location("audio_listen_wire", ROOT / "runtime" / "main.py")
        main = importlib.util.module_from_spec(main_spec)
        main_spec.loader.exec_module(main)
        context = {key: "synthetic" for key in ("account", "thread", "requester", "purpose", "request_key")}
        incoming = [
            {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"_meta": {"wechat/configuration": {"values": CONFIG}}}},
            {"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "media.listen", "arguments": {"audio": AUDIO, "prompt": "概述"}, "_meta": {"wechat/context": context}}},
            {"jsonrpc": "2.0", "id": "media-model-1", "result": REPLY},
        ]
        output = io.StringIO()
        main.Wire(io.StringIO("".join(json.dumps(row) + "\n" for row in incoming)), output).serve()
        rows = [json.loads(row) for row in output.getvalue().splitlines()]
        self.assertEqual(len(rows), 3)
        self.assertEqual(rows[1]["method"], "wechat/model_request")
        self.assertEqual(rows[1]["params"]["route"], "audio-review")
        self.assertEqual(rows[2]["result"]["sources"], [AUDIO])


if __name__ == "__main__":
    unittest.main()
