import importlib.util
from pathlib import Path
import unittest

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

if __name__ == "__main__": unittest.main()
