"""Offline contract checks for independently configured media capabilities."""
import importlib.util
import json
from pathlib import Path
import unittest


ROOT = Path(__file__).parents[1]

class ConfigurationContractTests(unittest.TestCase):
    def setUp(self):
        self.schema = json.loads((ROOT / "config.schema.json").read_text(encoding="utf8"))
        self.manifest = json.loads((ROOT / "plugin.json").read_text(encoding="utf8"))

    def test_capability_credentials_are_not_global_requirements(self):
        self.assertNotIn("required", self.schema)
        routes = {route["id"]: route for route in self.manifest["http_routes"]}
        self.assertTrue(routes["vision"]["optional"])
        self.assertTrue(routes["transcription"]["optional"])
        self.assertTrue(routes["audio-review"]["optional"])
        for name in ("vision_endpoint", "vision_key", "vision_model", "asr_endpoint", "asr_key", "asr_model", "audio_review_endpoint", "audio_review_key", "audio_review_model"):
            self.assertIn(name, self.schema["properties"])
        self.assertEqual(self.schema["properties"]["asr_backend"]["default"], "remote")
        profile = self.schema["properties"]["local_asr_profile_id"]
        self.assertEqual((profile["minLength"], profile["maxLength"]), (1, 128))

    def test_staged_ready_scope_is_exactly_observe_and_transcribe(self):
        self.assertEqual((self.manifest["version"], self.manifest["status"]), ("0.2.0", "ready"))
        capabilities = json.loads((ROOT / "capabilities.json").read_text(encoding="utf8"))["capabilities"]
        self.assertEqual([item["id"] for item in capabilities], ["media.observe", "media.transcribe", "media.listen"])
        for item in capabilities:
            self.assertEqual(item["implementation"], "ready")
            self.assertTrue(item["discoverable"])

    def test_vision_only_runtime_has_no_asr_dependency(self):
        spec = importlib.util.spec_from_file_location("media_observation_config", ROOT / "runtime" / "observation.py")
        observation = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(observation)
        image = {"media_ref": "frame_1", "sha256": "a" * 64, "version": 1, "mime": "image/png"}
        config = {"vision_model": "synthetic-vision", "vision_backend": "deepseek"}
        output = observation.observe({"images": [image]}, config, lambda _: {"choices": [{"message": {"content": "synthetic"}, "finish_reason": "stop"}]})
        self.assertEqual(output["status"], "observed")

    def test_remote_and_local_asr_do_not_require_vision_values(self):
        spec = importlib.util.spec_from_file_location("media_transcription_config", ROOT / "runtime" / "transcription.py")
        transcription = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(transcription)
        audio = {"media_ref": "audio_1", "sha256": "a" * 64, "version": 1, "mime": "audio/wav"}
        remote = transcription.transcribe({"audio": audio}, {"asr_model": "synthetic-asr"}, {}, lambda method, _: {"output": {"text": "remote"}})
        local = transcription.transcribe({"audio": audio}, {"asr_backend": "local", "local_asr_profile_id": "sensevoice-small-int8"}, {"local_asr": {"profile_id": "sensevoice-small-int8", "resource_version": "1"}}, lambda method, _: {"state": "transcribed", "text": "local"})
        self.assertEqual((remote["text"], local["text"]), ("remote", "local"))

    def test_audio_review_runtime_has_no_vision_or_asr_dependency(self):
        spec = importlib.util.spec_from_file_location("media_audio_observation_config", ROOT / "runtime" / "audio_observation.py")
        audio_observation = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(audio_observation)
        audio = {"media_ref": "audio_1", "sha256": "a" * 64, "version": 1, "mime": "audio/wav"}
        result = audio_observation.observe({"audio": audio, "prompt": "概述"}, {"audio_review_model": "synthetic-listen"}, lambda _: {"choices": [{"message": {"content": "synthetic"}, "finish_reason": "stop"}]})
        self.assertEqual(result["status"], "observed")


if __name__ == "__main__":
    unittest.main()
