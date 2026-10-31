import importlib.util
from pathlib import Path
import unittest
import io
import json

spec = importlib.util.spec_from_file_location("transcription", Path(__file__).parents[1] / "runtime/transcription.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

class TranscriptionTest(unittest.TestCase):
    def setUp(self):
        self.audio = {"media_ref": "audio_1", "sha256": "a"*64, "version": 2, "mime": "audio/wav"}
        self.calls = []
    def run_reply(self, reply, **args):
        def request(body):
            self.calls.append(body)
            return reply
        return module.transcribe({"audio": self.audio, **args}, {"asr_model": "synthetic-asr"}, request)
    def test_protocol_and_source(self):
        result = self.run_reply({"output": {"text": "synthetic words"}}, timeout_seconds=400)
        self.assertEqual(result["sources"], [self.audio])
        self.assertEqual(result["text"], "synthetic words")
        self.assertEqual(self.calls[0]["timeout_seconds"], 400)
        self.assertEqual(self.calls[0]["body"]["model"], "synthetic-asr")
        self.assertEqual(self.calls[0]["body"]["input"]["messages"][0]["content"][0]["input_audio"]["data"], {"$media_ref": self.audio})
    def test_nested_and_long_text(self):
        text = "合成" * 10000
        self.assertEqual(self.run_reply({"output": {"output": {"text": text}}})["text"], text)
    def test_no_words_is_not_failure_or_invented_text(self):
        result = self.run_reply({"code": "ASR_RESPONSE_HAVE_NO_WORDS"})
        self.assertEqual(result["status"], "no_recognizable_speech")
        self.assertEqual(result["text"], "")
    def test_invalid_responses(self):
        for reply in ({}, {"output": {"text": " "}}, {"code": "secret-provider-error"}, {"output": {"text": 1}}, []):
            with self.assertRaises(ValueError): self.run_reply(reply)
    def test_reject_overrides_before_request(self):
        for args in ({"model": "other"}, {"path": "C:/private.wav"}, {"timeout_seconds": True}):
            with self.assertRaises(ValueError): self.run_reply({}, **args)
        self.assertEqual(self.calls, [])
    def test_invalid_reference(self):
        self.audio["version"] = True
        with self.assertRaises(ValueError): self.run_reply({})
        self.assertEqual(self.calls, [])



class TranscriptionWireTest(unittest.TestCase):
    def test_stdio_dispatch_reference_and_reply(self):
        spec = importlib.util.spec_from_file_location("media_asr_wire", Path(__file__).parents[1] / "runtime/main.py")
        main = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(main)
        ref = {"media_ref": "audio_1", "sha256": "a"*64, "version": 1, "mime": "audio/wav"}
        context = {k: "synthetic" for k in ("account", "thread", "requester", "purpose", "request_key")}
        incoming = [
            {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"_meta": {"wechat/configuration": {"values": {"asr_model": "synthetic-asr"}}}}},
            {"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "media.transcribe", "arguments": {"audio": ref}, "_meta": {"wechat/context": context}}},
            {"jsonrpc": "2.0", "id": "media-model-1", "result": {"output": {"text": "synthetic transcript"}}},
        ]
        output = io.StringIO()
        main.Wire(io.StringIO("".join(json.dumps(x)+"\n" for x in incoming)), output).serve()
        replies = [json.loads(x) for x in output.getvalue().splitlines()]
        self.assertEqual(len(replies), 3)
        self.assertEqual(replies[1]["method"], "wechat/model_request")
        self.assertEqual(replies[1]["params"]["route"], "transcription")
        self.assertEqual(replies[2]["id"], 2)
        self.assertEqual(replies[2]["result"]["text"], "synthetic transcript")
        self.assertEqual(replies[2]["result"]["sources"], [ref])

if __name__ == "__main__": unittest.main()
