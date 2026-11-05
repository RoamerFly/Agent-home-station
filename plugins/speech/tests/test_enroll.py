import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import sys

sys.dont_write_bytecode = True  # Keep the signed package free of generated modules.
import tempfile
import unittest
import wave


RUNNER = Path(__file__).parents[1] / "runtime" / "enroll.py"


def checksum(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


class EnrollmentProtocolTests(unittest.TestCase):
    def fixture(self, directory, action="prepare"):
        root = Path(directory)
        audio = root / "sample.wav"
        with wave.open(str(audio), "wb") as stream:
            stream.setnchannels(1)
            stream.setsampwidth(2)
            stream.setframerate(16000)
            stream.writeframes(b"\x00\x00" * 16000)
        raw = {"protocol_version": 1, "action": action, "job_id": "job-synthetic",
               "profile_id": "voice-synthetic", "revision": 1, "preview_text": "这是一句未见的试听文本。",
               "samples": [{"audio": "sample.wav", "sha256": checksum(audio), "message_id": "synthetic-message", "transcript": "这是参考录音。"}]}
        path = root / "manifest.json"
        path.write_text(json.dumps(raw, ensure_ascii=False), encoding="utf8")
        return path, raw

    def execute(self, manifest):
        return subprocess.run([sys.executable, "-B", str(RUNNER), "--manifest", str(manifest)], text=True, capture_output=True)

    def test_prepare_records_only_hashes_and_decoded_duration(self):
        with tempfile.TemporaryDirectory() as directory:
            manifest, _ = self.fixture(directory)
            done = self.execute(manifest)
            self.assertEqual(done.returncode, 0, done.stderr)
            result = json.loads((Path(directory) / "result.json").read_text(encoding="utf8"))
            self.assertEqual(result["state"], "prepared")
            self.assertEqual(result["sample_count"], 1)
            self.assertEqual(result["sample_seconds"], 1.0)
            self.assertEqual(result["quality"], "decoded_local_vad_only")
            self.assertEqual(result["speaker_check"], "requires_diarization")
            self.assertFalse((Path(directory) / "preview.wav").exists())

    def test_tampered_input_is_not_accepted(self):
        with tempfile.TemporaryDirectory() as directory:
            manifest, raw = self.fixture(directory)
            raw["samples"][0]["sha256"] = "0" * 64
            manifest.write_text(json.dumps(raw), encoding="utf8")
            done = self.execute(manifest)
            self.assertEqual(done.returncode, 2)
            result = json.loads((Path(directory) / "result.json").read_text(encoding="utf8"))
            self.assertEqual(result["state"], "failed")
            self.assertEqual(result["reason"], "sample_hash_mismatch")

    def test_cancel_is_revision_bound(self):
        with tempfile.TemporaryDirectory() as directory:
            manifest, raw = self.fixture(directory, "cancel")
            done = self.execute(manifest)
            self.assertEqual(done.returncode, 0, done.stderr)
            marker = json.loads((Path(directory) / "cancel.json").read_text(encoding="utf8"))
            self.assertEqual(marker["job_id"], raw["job_id"])
            self.assertEqual(marker["revision"], raw["revision"])

    def test_owner_attestation_is_reported_as_distinct_from_analysis(self):
        with tempfile.TemporaryDirectory() as directory:
            manifest, raw = self.fixture(directory)
            sample = raw["samples"][0]
            sample["owner_attestation"] = {"kind": "owner_attested_single_speaker", "audio_sha256": sample["sha256"],
                                             "message_id": sample["message_id"], "instruction_message_id": "owner-request"}
            manifest.write_text(json.dumps(raw, ensure_ascii=False), encoding="utf8")
            done = self.execute(manifest)
            self.assertEqual(done.returncode, 0, done.stderr)
            result = json.loads((Path(directory) / "result.json").read_text(encoding="utf8"))
            self.assertEqual(result["speaker_check"], "owner_attested")

    def test_lock_uses_pinned_official_lfs_digests(self):
        lock = json.loads((Path(__file__).parents[1] / "base_models" / "enrollment-v2.json").read_text(encoding="utf8"))
        self.assertEqual(lock["engine_version"], "v2")
        self.assertEqual(lock["gpt"]["sha256"], "732f94e63b148066e24c7f9d2637f3374083e637635f07fbdb695dee20ddbe1f")
        self.assertEqual(lock["sovits"]["sha256"], "924fdccaa3c574bf139c25c9759aa1ed3b3f99e19a7c529ee996c2bc17663695")
        self.assertEqual(lock["discriminator"]["sha256"], "8ae7fe8dd8c8f2e718de359e00edac88b0c71ab2fd10b07ad4cc45070eb8a836")


if __name__ == "__main__":
    unittest.main()
