import importlib.util
from pathlib import Path
import unittest
import io
import json

spec = importlib.util.spec_from_file_location("transcription", Path(__file__).parents[1] / "runtime/transcription.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


def local_metadata(**changes):
    value = {"profile_id": "sensevoice-small-int8", "resource_version": "2026.10.31"}
    value.update(changes)
    return {"local_asr": value}


class TranscriptionTest(unittest.TestCase):
    def setUp(self):
        self.audio = {"media_ref": "audio_1", "sha256": "a"*64, "version": 2, "mime": "audio/wav"}
        self.calls = []

    def local_metadata(self, **changes):
        return local_metadata(**changes)
    def run_reply(self, reply, config=None, metadata=None, **args):
        def request(method, body):
            self.calls.append((method, body))
            return reply
        return module.transcribe({"audio": self.audio, **args}, config or {"asr_model": "synthetic-asr"}, metadata or {}, request)
    def test_protocol_and_source(self):
        result = self.run_reply({"output": {"text": "synthetic words"}}, timeout_seconds=400)
        self.assertEqual(result["sources"], [self.audio])
        self.assertEqual(result["text"], "synthetic words")
        self.assertEqual(self.calls[0][0], "wechat/model_request")
        self.assertEqual(self.calls[0][1]["timeout_seconds"], 400)
        self.assertEqual(self.calls[0][1]["body"]["model"], "synthetic-asr")
        self.assertEqual(self.calls[0][1]["body"]["input"]["messages"][0]["content"][0]["input_audio"]["data"], {"$media_ref": self.audio})
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

    def test_provider_failure_has_one_bounded_request(self):
        with self.assertRaisesRegex(ValueError, "asr_provider_failed"):
            self.run_reply({"code": "upstream_failure"})
        self.assertEqual(len(self.calls), 1)
    def test_reject_overrides_before_request(self):
        for args in ({"model": "other"}, {"path": "C:/private.wav"}, {"timeout_seconds": True}):
            with self.assertRaises(ValueError): self.run_reply({}, **args)
        self.assertEqual(self.calls, [])
    def test_invalid_reference(self):
        self.audio["version"] = True
        with self.assertRaises(ValueError): self.run_reply({})
        self.assertEqual(self.calls, [])

    def test_local_uses_bound_profile_once(self):
        result = self.run_reply(
            {"state": "transcribed", "text": "local transcript"},
            config={"asr_backend": "local", "local_asr_profile_id": "sensevoice-small-int8"},
            metadata=self.local_metadata(),
        )
        self.assertEqual(result["text"], "local transcript")
        self.assertEqual(self.calls, [("wechat/local_transcription", {"profile_id": "sensevoice-small-int8", "resource_version": "2026.10.31", "audio": self.audio, "timeout_seconds": 180})])

    def test_local_no_words_and_invalid_metadata(self):
        result = self.run_reply(
            {"state": "no_recognizable_speech"},
            config={"asr_backend": "local", "local_asr_profile_id": "sensevoice-small-int8"},
            metadata=self.local_metadata(),
        )
        self.assertEqual(result["status"], "no_recognizable_speech")
        for metadata in ({}, self.local_metadata(profile_id="../bad"), self.local_metadata(path="C:/private")):
            with self.assertRaisesRegex(ValueError, "local_asr_unavailable"):
                self.run_reply({}, config={"asr_backend": "local", "local_asr_profile_id": "sensevoice-small-int8"}, metadata=metadata)
        self.assertEqual(len(self.calls), 1)

    def test_local_rejects_result_or_backend_without_retry(self):
        metadata = self.local_metadata()
        for reply in ({}, {"state": "transcribed", "text": " "}, {"state": "no_recognizable_speech", "text": "invented"}, {"state": "unknown", "text": "x"}, {"state": "transcribed", "text": "x", "path": "C:/private"}):
            with self.assertRaisesRegex(ValueError, "local_asr_response_invalid"):
                self.run_reply(reply, config={"asr_backend": "local", "local_asr_profile_id": "sensevoice-small-int8"}, metadata=metadata)
        with self.assertRaisesRegex(ValueError, "asr_backend_not_configured"):
            self.run_reply({}, config={"asr_backend": "automatic"})
        self.assertEqual(len(self.calls), 5)

    def test_local_requires_matching_configured_profile_before_host_call(self):
        for config, metadata, code in (
            ({"asr_backend": "local"}, self.local_metadata(), "local_asr_profile_not_configured"),
            ({"asr_backend": "local", "local_asr_profile_id": "../outside"}, self.local_metadata(), "local_asr_profile_not_configured"),
            ({"asr_backend": "local", "local_asr_profile_id": "different-profile"}, self.local_metadata(), "local_asr_profile_mismatch"),
        ):
            with self.assertRaisesRegex(ValueError, code):
                self.run_reply({}, config=config, metadata=metadata)
        self.assertEqual(self.calls, [])

    def test_remote_does_not_require_local_profile(self):
        result = self.run_reply({"output": {"text": "remote transcript"}}, config={"asr_model": "synthetic-asr"})
        self.assertEqual(result["text"], "remote transcript")



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

    def test_stdio_invalid_audio_never_calls_host(self):
        spec = importlib.util.spec_from_file_location("media_asr_invalid", Path(__file__).parents[1] / "runtime/main.py")
        main = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(main)
        context = {k: "synthetic" for k in ("account", "thread", "requester", "purpose", "request_key")}
        incoming = [
            {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"_meta": {"wechat/configuration": {"values": {"asr_model": "synthetic-asr"}}}}},
            {"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "media.transcribe", "arguments": {"audio": {"media_ref": "wrong"}}, "_meta": {"wechat/context": context}}},
        ]
        output = io.StringIO()
        main.Wire(io.StringIO("".join(json.dumps(x)+"\n" for x in incoming)), output).serve()
        replies = [json.loads(x) for x in output.getvalue().splitlines()]
        self.assertEqual(len(replies), 2)
        self.assertEqual(replies[1]["result"]["error"], "invalid_audio_reference")

    def test_stdio_local_dispatch_without_path_metadata(self):
        spec = importlib.util.spec_from_file_location("media_asr_local_wire", Path(__file__).parents[1] / "runtime/main.py")
        main = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(main)
        ref = {"media_ref": "audio_1", "sha256": "a"*64, "version": 1, "mime": "audio/wav"}
        context = {k: "synthetic" for k in ("account", "thread", "requester", "purpose", "request_key")}
        incoming = [
            {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"_meta": {"wechat/configuration": {"values": {"asr_backend": "local", "local_asr_profile_id": "sensevoice-small-int8"}}, "wechat/host_metadata": local_metadata()}}},
            {"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "media.transcribe", "arguments": {"audio": ref}, "_meta": {"wechat/context": context}}},
            {"jsonrpc": "2.0", "id": "media-model-1", "result": {"state": "transcribed", "text": "local transcript"}},
        ]
        output = io.StringIO()
        main.Wire(io.StringIO("".join(json.dumps(x)+"\n" for x in incoming)), output).serve()
        replies = [json.loads(x) for x in output.getvalue().splitlines()]
        self.assertEqual(len(replies), 3)
        self.assertEqual(replies[1]["method"], "wechat/local_transcription")
        self.assertEqual(set(replies[1]["params"]), {"profile_id", "resource_version", "audio", "timeout_seconds"})
        self.assertNotIn("path", json.dumps(replies[1]["params"]))
        self.assertEqual(replies[2]["result"]["text"], "local transcript")

if __name__ == "__main__": unittest.main()
