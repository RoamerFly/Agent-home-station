"""Pure preparation rules: no host callback, network, GPU, or media access."""
import importlib.util
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("music_preparation", ROOT / "runtime/preparation.py")
preparation = importlib.util.module_from_spec(spec)
spec.loader.exec_module(preparation)

SHA = "a" * 64


class PreparationTests(unittest.TestCase):
    def rvc(self, **changes):
        value = {"action": "rvc_request", "health": {"ready": True, "bridge_version": 13, "voice_profiles": True, "private_voice_profiles": True}, "title": "原曲", "voice_profile_id": "private-mom", "reference_sha256": SHA, "reference_start_seconds": 1.5, "reference_seconds": 12.0, "audio_ref": {"ref": "reference_0", "sha256": "b" * 64, "bytes": 123}}
        value.update(changes)
        return value

    def test_style_is_faithful_and_replaces_gender_tag(self):
        self.assertEqual(preparation.prepare({"action": "style", "style": "女声, 民谣", "gender": "female", "mode": "original"}), {"style": "民谣, female vocal"})
        cover = preparation.prepare({"action": "style", "style": "男声 摇滚", "gender": "female", "mode": "cover"})["style"]
        self.assertIn(preparation.FAITHFUL_COVER_STYLE, cover)
        self.assertTrue(cover.endswith("female vocal"))

    def test_rvc_request_binds_only_host_audio_reference(self):
        out = preparation.prepare(self.rvc())
        self.assertEqual(out["method"], "POST")
        self.assertEqual(out["path"], "/rvc-jobs")
        self.assertEqual(out["body"]["reference_base64"], {"$music_ref": self.rvc()["audio_ref"]})
        self.assertTrue(out["body"]["reference_pretrimmed"])
        for changed, code in (({"health": {"ready": True, "bridge_version": 8, "voice_profiles": True, "private_voice_profiles": True}}, "rvc_bridge_not_ready"), ({"health": {"ready": True, "bridge_version": 11, "voice_profiles": False, "private_voice_profiles": True}}, "rvc_profile_bridge_required"), ({"health": {"ready": True, "bridge_version": 12, "voice_profiles": True, "private_voice_profiles": False}}, "rvc_private_profile_bridge_required"), ({"audio_ref": {"ref": "/private/file", "sha256": SHA, "bytes": 1}}, "music_preparation_invalid")):
            with self.subTest(changed=changed), self.assertRaisesRegex(ValueError, code):
                preparation.prepare(self.rvc(**changed))

    def test_legacy_v9_health_allows_default_profile_but_not_selected_voice(self):
        legacy = {"ready": True, "bridge_version": 9}
        out = preparation.prepare(self.rvc(health=legacy, voice_profile_id=""))
        self.assertNotIn("voice_profile_id", out["body"])
        with self.assertRaisesRegex(ValueError, "rvc_profile_bridge_required"):
            preparation.prepare(self.rvc(health=legacy, voice_profile_id="registered-rvc"))

    def test_resume_plans_and_results_never_change_identity(self):
        base = {"action": "resume_plan", "id": "job-1", "status": {"id": "job-1", "engine": "yue", "state": "failed"}, "restart_missing": True, "restart_failed": True}
        self.assertEqual(preparation.prepare(base), {"engine": "yue", "state": "failed", "submit": True, "options": {"restart_missing": True, "restart_failed": True}})
        base["status"]["state"] = "running"
        self.assertFalse(preparation.prepare(base)["submit"])
        with self.assertRaisesRegex(ValueError, "music_resume_restart_invalid"):
            preparation.prepare({"action": "resume_plan", "id": "job-1", "status": {"id": "job-1", "engine": "rvc", "state": "failed"}, "restart_missing": True, "restart_failed": False})
        result = {"action": "resume_result", "id": "job-1", "engine": "rvc", "status": {"id": "job-1", "engine": "rvc", "state": "running"}}
        self.assertEqual(preparation.prepare(result), {"status": result["status"]})
        result["status"]["id"] = "other"
        with self.assertRaisesRegex(ValueError, "music_resume_identity_invalid"):
            preparation.prepare(result)

    def test_recovery_recipe_preserves_verified_recipe_and_upgrades_source_hash(self):
        status = {"id": "job-1", "state": "completed", "reference_used": True, "recipe": {"title": "翻唱", "mode": "cover", "engine": "", "lyrics": "原词", "reference_sha256": "b" * 64, "reference_source_sha256": SHA}}
        out = preparation.prepare({"action": "recover_recipe", "id": "job-1", "status": status})
        self.assertEqual(out["recipe"]["reference_sha256"], SHA)
        for bad in ({"id": "job-1", "state": "completed", "recipe": {"title": "", "mode": "original", "lyrics": "x"}}, {"id": "job-1", "state": "completed", "reference_used": False, "recipe": {"title": "x", "mode": "cover", "lyrics": "x", "reference_sha256": SHA}}):
            with self.subTest(bad=bad), self.assertRaisesRegex(ValueError, "music_recovery_recipe_invalid"):
                preparation.prepare({"action": "recover_recipe", "id": "job-1", "status": bad})

    def test_unknown_fields_nonfinite_and_paths_are_rejected(self):
        for value in ({"action": "style", "style": "x", "gender": "female", "mode": "original", "target": "x"}, self.rvc(reference_seconds=float("nan")), {"action": "resume_result", "id": "job-1", "engine": "rvc", "status": {"id": "job-1", "engine": "rvc", "path": "C:/private"}}):
            with self.subTest(value=str(value)), self.assertRaises(ValueError):
                preparation.prepare(value)


if __name__ == "__main__":
    unittest.main()
