"""Synthetic cloud-music contract tests; no provider, credential, network or media access."""
import copy
import importlib.util
import io
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("music_main", Path(__file__).resolve().parents[1] / "runtime/main.py")
main = importlib.util.module_from_spec(spec)
spec.loader.exec_module(main)
generate = main.cloud_music.generate

CONFIG = {"music_model": "synthetic-music"}
REPLY = {"model": "synthetic-music", "output": {"audio": {"url": "https://media.example.test/song.mp3?token=opaque"}}}


class CloudMusicTests(unittest.TestCase):
    def test_binds_model_female_mp3_and_optional_lyrics_once(self):
        captured = []
        out = generate({"prompt": "轻快的中文流行曲", "lyrics": "第一句", "timeout_seconds": 61}, CONFIG, lambda request: captured.append(request) or copy.deepcopy(REPLY))
        self.assertEqual(captured, [{"route": "generate", "body": {"model": "synthetic-music", "input": {"prompt": "轻快的中文流行曲", "gender": "female", "format": "mp3", "lyrics": "第一句"}}, "max_bytes": 1024 * 1024, "timeout_seconds": 61}])
        self.assertEqual(out["status"], "provider_accepted")
        self.assertEqual(out["coverage"], "provider_music_response")
        self.assertTrue(out["untrusted_external_content"])
        self.assertNotIn("music_key", json.dumps(captured, ensure_ascii=False))
        self.assertNotIn("voice", out)

    def test_accepts_old_unicode_limit_and_preserves_explicit_timeout(self):
        prompt = "音" * 2000
        captured = []
        generate({"prompt": prompt, "timeout_seconds": 360}, CONFIG, lambda request: captured.append(request) or copy.deepcopy(REPLY))
        self.assertEqual(captured[0]["body"]["input"]["prompt"], prompt)
        self.assertEqual(captured[0]["timeout_seconds"], 360)
        generate({"prompt": "", "lyrics": "词" * 2000}, CONFIG, lambda request: copy.deepcopy(REPLY))

    def test_rejects_untrusted_voice_or_invalid_arguments_before_provider(self):
        invalid = ({}, {"prompt": ""}, {"prompt": "x", "voice_id": "mom"}, {"prompt": "x", "gender": "male"}, {"prompt": "x", "timeout_seconds": True}, {"prompt": "x" * 2001}, {"prompt": "x", "lyrics": "词" * 2001})
        for args in invalid:
            with self.subTest(args=str(args)[:64]), self.assertRaises(ValueError):
                generate(args, CONFIG, lambda _: self.fail("provider called"))

    def test_provider_error_or_unsafe_audio_url_never_reports_success(self):
        for reply in ({"error": {"private": "detail"}}, {}, {"output": {"audio": {"url": "file:///private/song.mp3"}}}, {"output": {"audio": {"url": "https://user:pass@media.example.test/song.mp3"}}}, {"output": {"audio": {"url": "https://media.example.test/song.mp3#fragment"}}}):
            with self.subTest(reply=reply), self.assertRaises(ValueError):
                generate({"prompt": "你好"}, CONFIG, lambda _: copy.deepcopy(reply))

    def test_stdio_requires_bound_context_and_uses_one_host_request(self):
        context = {key: "synthetic" for key in ("account", "thread", "requester", "purpose", "request_key")}
        messages = [
            {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"_meta": {"wechat/configuration": {"values": CONFIG}}}},
            {"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "music.generate", "arguments": {"prompt": "中文歌"}, "_meta": {"wechat/context": context}}},
            {"jsonrpc": "2.0", "id": "music-model-1", "result": REPLY},
        ]
        output = io.StringIO()
        main.Wire(io.StringIO("\n".join(json.dumps(item, ensure_ascii=False) for item in messages) + "\n"), output).serve()
        rows = [json.loads(line) for line in output.getvalue().splitlines()]
        self.assertEqual(rows[1]["method"], "wechat/model_request")
        self.assertEqual(rows[1]["params"]["route"], "generate")
        self.assertEqual(rows[2]["result"]["status"], "provider_accepted")
        self.assertNotIn("music_key", output.getvalue())

    def test_stdio_lists_only_ready_cloud_generation_tool(self):
        messages = [{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"_meta": {"wechat/configuration": {"values": CONFIG}}}}, {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}}]
        output = io.StringIO()
        main.Wire(io.StringIO("\n".join(json.dumps(item) for item in messages) + "\n"), output).serve()
        rows = [json.loads(line) for line in output.getvalue().splitlines()]
        self.assertEqual([tool["name"] for tool in rows[1]["result"]["tools"]], ["music.generate"])

    def test_duplicate_json_frame_is_rejected_without_host_request(self):
        output = io.StringIO()
        main.Wire(io.StringIO('{"jsonrpc":"2.0","id":1,"id":2,"method":"ping"}\n'), output).serve()
        rows = [json.loads(line) for line in output.getvalue().splitlines()]
        self.assertEqual(rows, [{"jsonrpc": "2.0", "id": None, "error": {"code": -32600, "message": "invalid_request_or_page"}}])


if __name__ == "__main__":
    unittest.main()
