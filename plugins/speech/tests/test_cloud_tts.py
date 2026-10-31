"""Synthetic cloud-TTS protocol tests; no provider, credential or media fetch."""
import copy
import importlib.util
import io
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("speech_main", Path(__file__).resolve().parents[1] / "runtime/main.py")
main = importlib.util.module_from_spec(spec)
spec.loader.exec_module(main)
synthesize = main.cloud_tts.synthesize

CONFIG = {"tts_model": "synthetic-tts", "default_voice": "synthetic-voice", "default_instruction": "自然、清晰"}
REPLY = {"model": "synthetic-tts", "output": {"audio": {"url": "https://media.example.test/speech.wav"}}}


class CloudTTSTests(unittest.TestCase):
    def test_binds_model_voice_wav_and_instruction_without_secrets(self):
        captured = []
        out = synthesize({"text": "你好", "instruction": "语速稍慢", "timeout_seconds": 19}, CONFIG, lambda request: captured.append(request) or copy.deepcopy(REPLY))
        request = captured[0]
        self.assertEqual(request["route"], "synthesize")
        self.assertEqual(request["timeout_seconds"], 19)
        self.assertEqual(request["body"], {"model": "synthetic-tts", "input": {"text": "你好", "voice": "synthetic-voice", "format": "wav", "sample_rate": 24000, "instruction": "自然、清晰\n本次表达要求（保持上述角色声线，只调整语气与节奏）：语速稍慢"}})
        self.assertEqual(out["provider_audio_url"], REPLY["output"]["audio"]["url"])
        self.assertTrue(out["untrusted_external_content"])
        self.assertNotIn("tts_key", json.dumps(request, ensure_ascii=False))

    def test_uses_default_instruction_and_rejects_untrusted_argument_overrides(self):
        captured = []
        synthesize({"text": "你好"}, CONFIG, lambda request: captured.append(request) or copy.deepcopy(REPLY))
        self.assertEqual(captured[0]["body"]["input"]["instruction"], "自然、清晰")
        captured = []
        synthesize({"text": "你好", "instruction": "自然、清晰"}, CONFIG, lambda request: captured.append(request) or copy.deepcopy(REPLY))
        self.assertEqual(captured[0]["body"]["input"]["instruction"], "自然、清晰")
        for args in ({}, {"text": ""}, {"text": "x", "voice": "other"}, {"text": "x", "model": "other"}, {"text": "x", "timeout_seconds": True}, {"text": "x", "instruction": "x" * 1501}):
            with self.subTest(args=args), self.assertRaises(ValueError):
                synthesize(args, CONFIG, lambda _: self.fail("provider called"))

    def test_provider_failure_or_unsafe_result_is_not_a_success(self):
        for reply in ({"error": {"private": "provider detail"}}, {}, {"output": {"audio": {"url": "file:///private.wav"}}}, {"output": {"audio": {"url": "https://a b"}}}):
            with self.subTest(reply=reply), self.assertRaises(ValueError):
                synthesize({"text": "你好"}, CONFIG, lambda _: reply)

    def test_stdio_has_scoped_context_and_no_host_only_config(self):
        context = {k: "synthetic" for k in ("account", "thread", "requester", "purpose", "request_key")}
        messages = [
            {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"_meta": {"wechat/configuration": {"values": CONFIG}}}},
            {"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "speech.synthesize", "arguments": {"text": "你好"}, "_meta": {"wechat/context": context}}},
            {"jsonrpc": "2.0", "id": "speech-model-1", "result": REPLY},
        ]
        output = io.StringIO()
        main.Wire(io.StringIO("\n".join(json.dumps(item) for item in messages) + "\n"), output).serve()
        rows = [json.loads(line) for line in output.getvalue().splitlines()]
        self.assertEqual(rows[1]["method"], "wechat/model_request")
        self.assertEqual(rows[2]["result"]["status"], "provider_accepted")
        self.assertNotIn("tts_key", output.getvalue())


if __name__ == "__main__":
    unittest.main()
