import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from contextlib import redirect_stdout
from unittest import mock


MODULE = Path(__file__).with_name("plugin_migration_preview.py")
SPEC = importlib.util.spec_from_file_location("plugin_migration_preview", MODULE)
preview = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(preview)


class PluginMigrationPreviewTests(unittest.TestCase):
    def make_repo(self, base):
        repo = base / "repo"
        for plugin, status in (("speech", "ready"), ("media-understanding", "ready"), ("music", "ready"), ("web-search", "ready"), ("share-reader", "migration-pending")):
            directory = repo / "plugins" / plugin
            directory.mkdir(parents=True)
            manifest = {"id": plugin, "version": "1.2.3", "status": status}
            if plugin == "music":
                manifest["permissions"] = ["model.invoke"]
            (directory / "plugin.json").write_text(json.dumps(manifest), encoding="utf8")
        return repo

    def make_root(self, base):
        root = base / "admin"
        root.mkdir()
        secret = "synthetic-secret-never-report"
        (root / "config.json").write_text(json.dumps({"provider": "deepseek", "base_url": "https://api.deepseek.com", "api_key": secret, "model": "vision-a", "max_tokens": 900, "thinking_enabled": True, "reasoning_effort": "high"}), encoding="utf8")
        (root / "speech-config.json").write_text(json.dumps({"endpoint": "https://dashscope.aliyuncs.com", "key": secret, "tts_enabled": True, "tts_model": "tts-a", "voice": "voice-a", "instruction": "calm", "asr_enabled": True, "asr_backend": "local", "asr_local_model": "sensevoice-test", "music_enabled": True, "music_model": "music-a"}), encoding="utf8")
        (root / "mcp-config.json").write_text(json.dumps({"enabled": True, "web_search_enabled": True, "web_fetch_enabled": True}), encoding="utf8")
        (root / "channels-config.json").write_text(json.dumps({"enabled": True, "cookie": "synthetic-cookie", "assistant_cookie": "synthetic-assistant", "object_bridge_url": "https://bridge.example"}), encoding="utf8")
        account = "secondary"
        account_dir = root / "accounts" / hashlib.sha256(account.encode()).hexdigest()
        account_dir.mkdir(parents=True)
        (account_dir / "config.json").write_text(json.dumps({"provider": "openai-compatible", "base_url": "https://example.invalid/v1", "api_key": "second-key", "model": "second"}), encoding="utf8")
        (root / "accounts.json").write_text(json.dumps([{"id": account}]), encoding="utf8")
        return root, secret

    def add_declared_deepseek_backend(self, repo):
        web = repo / "plugins" / "web-search"
        manifest = json.loads((web / "plugin.json").read_text(encoding="utf8"))
        manifest["http_routes"] = [{"id": "deepseek-search", "endpoint": "https://api.deepseek.com/v1/tools/search", "credential": "search_key", "credential_header": "Authorization", "credential_prefix": "Bearer ", "capabilities": ["web.search"]}]
        (web / "plugin.json").write_text(json.dumps(manifest), encoding="utf8")
        (web / "config.schema.json").write_text(json.dumps({"properties": {"search_backend": {"enum": ["public", "deepseek"]}, "search_key": {"type": "string", "x-secret": True, "x-host-only": True}}}), encoding="utf8")
        (web / "capabilities.json").write_text(json.dumps({"capabilities": [{"id": "web.search", "implementation": "ready", "permissions": ["network"]}]}), encoding="utf8")

    def add_declared_share_bridge(self, repo):
        share = repo / "plugins" / "share-reader"
        manifest = json.loads((share / "plugin.json").read_text(encoding="utf8"))
        manifest["status"] = "ready"
        (share / "plugin.json").write_text(json.dumps(manifest), encoding="utf8")
        (share / "config.schema.json").write_text(json.dumps({"type": "object", "properties": {"object_bridge_url": {"type": "string"}}, "additionalProperties": False}), encoding="utf8")

    def test_preview_is_per_account_read_only_and_redacts_secrets(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base)
            root, secret = self.make_root(base)
            before = {p.relative_to(root): p.read_bytes() for p in root.rglob("*") if p.is_file()}
            report, handoff = preview.build_preview(root, repo, "main")
            encoded = json.dumps(report, ensure_ascii=False)
            self.assertNotIn(secret, encoded)
            self.assertEqual([a["account"] for a in report["accounts"]], ["main", "secondary"])
            main = report["accounts"][0]
            self.assertEqual([step["plugin"] for step in main["plugins"]], ["media-understanding", "speech", "music", "web-search", "share-reader"])
            self.assertTrue(all(step["proposed_enabled"] is False for account in report["accounts"] for step in account["plugins"]))
            media = next(x for x in main["plugins"] if x["plugin"] == "media-understanding")
            self.assertIn("registered_local_asr_profile:sensevoice-test", media["required_missing"])
            self.assertEqual(media["values"]["local_asr_profile_id"], "sensevoice-test")
            speech = next(x for x in main["plugins"] if x["plugin"] == "speech")
            self.assertEqual(speech["values"]["tts_endpoint"], "https://dashscope.aliyuncs.com/api/v1/services/audio/tts/SpeechSynthesizer")
            web = next(x for x in main["plugins"] if x["plugin"] == "web-search")
            self.assertIn("official_deepseek_search_adapter_not_declared_by_web-search_plugin", web["required_missing"])
            self.assertFalse(web["proposed_enabled"])
            self.assertEqual(handoff["accounts"][0]["secrets"]["speech.tts_key"], secret)
            self.assertEqual(handoff["accounts"][0]["secrets"]["music.music_key"], secret)
            self.assertEqual(before, {p.relative_to(root): p.read_bytes() for p in root.rglob("*") if p.is_file()})

    def test_local_asr_default_and_custom_profile_are_config_values_but_still_blocked(self):
        for legacy, wanted in (("", "sensevoice-small-int8"), ("sensevoice-explicit", "sensevoice-explicit")):
            with self.subTest(profile=legacy), tempfile.TemporaryDirectory() as temporary:
                base = Path(temporary)
                repo = self.make_repo(base)
                root, secret = self.make_root(base)
                path = root / "speech-config.json"
                speech = json.loads(path.read_text(encoding="utf8"))
                speech["asr_local_model"] = legacy
                path.write_text(json.dumps(speech), encoding="utf8")
                before = {p.relative_to(root): p.read_bytes() for p in root.rglob("*") if p.is_file()}
                report, handoff = preview.build_preview(root, repo, "main")
                media = next(step for step in report["accounts"][0]["plugins"] if step["plugin"] == "media-understanding")
                self.assertEqual(media["values"]["asr_backend"], "local")
                self.assertEqual(media["values"]["local_asr_profile_id"], wanted)
                self.assertEqual(media["profile_id"], wanted)
                self.assertIn("registered_local_asr_profile:" + wanted, media["required_missing"])
                self.assertEqual(media["status"], "blocked")
                self.assertFalse(media["proposed_enabled"])
                self.assertFalse(media["eligible_to_enable_after_confirmation"])
                self.assertNotIn("asr_key", media["values"])
                self.assertNotIn("media-understanding.asr_key", handoff["accounts"][0]["secrets"])
                self.assertNotIn(secret, json.dumps(report, ensure_ascii=False))
                self.assertEqual(before, {p.relative_to(root): p.read_bytes() for p in root.rglob("*") if p.is_file()})
                self.assertEqual(json.loads(path.read_text(encoding="utf8"))["asr_local_model"], legacy)

    def test_non_local_asr_does_not_select_profile_or_silently_convert_fallback(self):
        for backend, enabled in (("bailian", True), ("local_then_bailian", True), ("local", False)):
            with self.subTest(backend=backend, enabled=enabled), tempfile.TemporaryDirectory() as temporary:
                base = Path(temporary)
                repo = self.make_repo(base)
                root, secret = self.make_root(base)
                path = root / "speech-config.json"
                speech = json.loads(path.read_text(encoding="utf8"))
                speech.update({"asr_backend": backend, "asr_enabled": enabled, "asr_local_model": "sensevoice-unused"})
                path.write_text(json.dumps(speech), encoding="utf8")
                original = path.read_bytes()
                report, handoff = preview.build_preview(root, repo, "main")
                media = next(step for step in report["accounts"][0]["plugins"] if step["plugin"] == "media-understanding")
                self.assertNotIn("local_asr_profile_id", media["values"])
                self.assertNotIn("profile_id", media)
                if backend == "local_then_bailian":
                    self.assertEqual(media["status"], "blocked")
                    self.assertIn("asr_backend:legacy_local_then_bailian_requires_explicit_choice", media["required_missing"])
                    self.assertNotIn("asr_backend", media["values"])
                    self.assertNotIn("media-understanding.asr_key", handoff["accounts"][0]["secrets"])
                elif backend == "bailian":
                    self.assertEqual(media["values"]["asr_backend"], "remote")
                    self.assertEqual(handoff["accounts"][0]["secrets"]["media-understanding.asr_key"], secret)
                self.assertEqual(path.read_bytes(), original)

    def test_local_profile_source_change_updates_new_plan_without_mutating_old_plan(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base)
            root, _ = self.make_root(base)
            first, _ = preview.build_preview(root, repo, "main")
            before = next(step for step in first["accounts"][0]["plugins"] if step["plugin"] == "media-understanding")
            path = root / "speech-config.json"
            speech = json.loads(path.read_text(encoding="utf8"))
            speech["asr_local_model"] = "sensevoice-reselected"
            path.write_text(json.dumps(speech), encoding="utf8")
            original = path.read_bytes()
            second, _ = preview.build_preview(root, repo, "main")
            after = next(step for step in second["accounts"][0]["plugins"] if step["plugin"] == "media-understanding")
            self.assertEqual(before["values"]["local_asr_profile_id"], "sensevoice-test")
            self.assertEqual(after["values"]["local_asr_profile_id"], "sensevoice-reselected")
            self.assertNotEqual(before, after)
            self.assertEqual(path.read_bytes(), original)

    def test_music_step_maps_endpoint_model_and_only_private_secret_handoff(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base)
            root, secret = self.make_root(base)
            speech_path = root / "speech-config.json"
            speech = json.loads(speech_path.read_text(encoding="utf8"))
            speech["endpoint"] = "https://dashscope-intl.aliyuncs.com/"
            speech_path.write_text(json.dumps(speech), encoding="utf8")
            report, handoff = preview.build_preview(root, repo, "main")
            music = next(step for step in report["accounts"][0]["plugins"] if step["plugin"] == "music")
            self.assertEqual(music["values"], {"music_endpoint": "https://dashscope-intl.aliyuncs.com/api/v1/services/audio/music/generation", "music_model": "music-a"})
            self.assertEqual(music["status"], "ready_for_administrator_review")
            self.assertTrue(music["legacy_enabled"])
            self.assertTrue(music["eligible_to_enable_after_confirmation"])
            self.assertIs(music["proposed_enabled"], False)
            self.assertEqual(music["permissions"], ["model.invoke"])
            self.assertEqual(music["required_missing"], [])
            self.assertEqual(music["secrets"]["music_key"], {"source_field": "speech-config.json:key", "present": True, "sha256": hashlib.sha256(secret.encode()).hexdigest()})
            self.assertNotIn(secret, json.dumps(report, ensure_ascii=False))
            self.assertNotIn("music_key", music["values"])
            self.assertEqual(handoff["accounts"][0]["secrets"]["music.music_key"], secret)
            for field in ("tts_*", "asr_*", "yue_*", "yue_token", "named_voice_library", "rvc_profiles"):
                self.assertIn(field, music["unmapped_legacy_fields"])
            second = next(step for step in report["accounts"][1]["plugins"] if step["plugin"] == "music")
            self.assertFalse(second["legacy_enabled"])
            self.assertFalse(second["eligible_to_enable_after_confirmation"])
            self.assertNotIn("music.music_key", handoff["accounts"][1]["secrets"])

    def test_music_enabled_without_key_is_blocked_without_handoff(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base)
            root, _ = self.make_root(base)
            speech_path = root / "speech-config.json"
            speech = json.loads(speech_path.read_text(encoding="utf8"))
            speech.pop("key")
            speech_path.write_text(json.dumps(speech), encoding="utf8")
            before = {path.relative_to(root): path.read_bytes() for path in root.rglob("*") if path.is_file()}
            report, handoff = preview.build_preview(root, repo, "main")
            music = next(step for step in report["accounts"][0]["plugins"] if step["plugin"] == "music")
            self.assertEqual(music["status"], "blocked")
            self.assertIn("music_key", music["required_missing"])
            self.assertEqual(music["secrets"]["music_key"], {"source_field": "speech-config.json:key", "present": False})
            self.assertFalse(music["eligible_to_enable_after_confirmation"])
            self.assertIs(music["proposed_enabled"], False)
            self.assertNotIn("music.music_key", handoff["accounts"][0]["secrets"])
            self.assertEqual(before, {path.relative_to(root): path.read_bytes() for path in root.rglob("*") if path.is_file()})

    def test_real_music_package_contract_remains_disabled_in_public_preview(self):
        with tempfile.TemporaryDirectory() as temporary:
            root, secret = self.make_root(Path(temporary))
            repo = MODULE.resolve().parents[1]
            report, handoff = preview.build_preview(root, repo, "main")
            music = next(step for step in report["accounts"][0]["plugins"] if step["plugin"] == "music")
            digest = hashlib.sha256((repo / "plugins" / "music" / "plugin.json").read_bytes()).hexdigest()
            self.assertEqual(music["package"]["expected_package_digest"], digest)
            self.assertEqual(music["package"]["version"], "0.2.0")
            self.assertEqual(music["package"]["status"], "ready")
            self.assertEqual(music["permissions"], ["model.invoke"])
            self.assertIs(music["proposed_enabled"], False)
            self.assertNotIn(secret, json.dumps(report, ensure_ascii=False))
            self.assertEqual(handoff["accounts"][0]["secrets"]["music.music_key"], secret)

    def test_declared_deepseek_backend_preserves_provider_and_maps_secret(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base)
            self.add_declared_deepseek_backend(repo)
            root, secret = self.make_root(base)
            report, handoff = preview.build_preview(root, repo, "main")
            web = next(x for x in report["accounts"][0]["plugins"] if x["plugin"] == "web-search")
            self.assertEqual(web["values"]["search_backend"], "deepseek")
            self.assertNotIn("official_deepseek_search_adapter_not_declared_by_web-search_plugin", web["required_missing"])
            self.assertEqual(web["secrets"]["search_key"]["source_field"], "config.json:api_key")
            self.assertNotIn(secret, json.dumps(report, ensure_ascii=False))
            self.assertEqual(handoff["accounts"][0]["secrets"]["web-search.search_key"], secret)

    def test_declared_loopback_bridge_is_mapped_but_cookie_fallback_requires_choice(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base)
            self.add_declared_share_bridge(repo)
            root, _ = self.make_root(base)
            (root / "channels-config.json").write_text(json.dumps({"enabled": True, "cookie": "synthetic-cookie", "object_bridge_url": "http://127.0.0.1:3210/"}), encoding="utf8")
            report, _ = preview.build_preview(root, repo, "main")
            share = next(x for x in report["accounts"][0]["plugins"] if x["plugin"] == "share-reader")
            self.assertEqual(share["values"]["object_bridge_url"], "http://127.0.0.1:3210")
            self.assertIn("explicit_bridge_fallback_policy_needed", share["required_missing"])
            self.assertNotIn("channels-config.json:object_bridge_url", share.get("unmapped_legacy_fields", []))

    def test_invalid_bridge_is_not_mapped_even_when_schema_declares_one(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base)
            self.add_declared_share_bridge(repo)
            root, _ = self.make_root(base)
            (root / "channels-config.json").write_text(json.dumps({"enabled": True, "object_bridge_url": "https://bridge.example"}), encoding="utf8")
            report, _ = preview.build_preview(root, repo, "main")
            share = next(x for x in report["accounts"][0]["plugins"] if x["plugin"] == "share-reader")
            self.assertNotIn("object_bridge_url", share["values"])
            self.assertIn("object_bridge_url_invalid", share["required_missing"])

    def test_optional_outputs_are_new_private_files_outside_source(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base)
            root, secret = self.make_root(base)
            public = base / "plan.json"
            private = base / "host-secrets.json"
            if preview._host_secret_output_allowed():
                self.assertEqual(preview.main(["--admin-root", str(root), "--repo", str(repo), "--output", str(public), "--host-secrets-output", str(private)]), 0)
            else:
                with self.assertRaisesRegex(ValueError, "host_secrets_output_requires_verified_private_acl"):
                    preview.main(["--admin-root", str(root), "--repo", str(repo), "--output", str(public), "--host-secrets-output", str(private)])
                self.assertFalse(public.exists())
                self.assertFalse(private.exists())
                self.assertEqual(preview.main(["--admin-root", str(root), "--repo", str(repo), "--output", str(public)]), 0)
            self.assertFalse(secret in public.read_text(encoding="utf8"))
            if preview._host_secret_output_allowed():
                self.assertIn(secret, private.read_text(encoding="utf8"))
                self.assertEqual(os.stat(private).st_mode & 0o777, 0o600)
            with self.assertRaisesRegex(ValueError, "output_must_be_outside_source_root"):
                preview._write_new(root / "unsafe.json", {}, root)
            with self.assertRaisesRegex(ValueError, "output_must_be_new_file"):
                preview._write_new(public, {}, root)

    def test_secret_export_fails_closed_without_private_acl_but_public_plan_remains_available(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base)
            root, _ = self.make_root(base)
            public, private = base / "plan.json", base / "host-secrets.json"
            with mock.patch.object(preview, "_host_secret_output_allowed", return_value=False):
                with self.assertRaisesRegex(ValueError, "host_secrets_output_requires_verified_private_acl"):
                    preview.main(["--admin-root", str(root), "--repo", str(repo), "--output", str(public), "--host-secrets-output", str(private)])
                self.assertFalse(public.exists())
                self.assertFalse(private.exists())
                self.assertEqual(preview.main(["--admin-root", str(root), "--repo", str(repo), "--output", str(public)]), 0)
            self.assertTrue(public.exists())

    def test_stdout_does_not_emit_handoff(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base)
            root, secret = self.make_root(base)
            stream = io.StringIO()
            with redirect_stdout(stream):
                self.assertEqual(preview.main(["--admin-root", str(root), "--repo", str(repo)]), 0)
            self.assertNotIn(secret, stream.getvalue())


if __name__ == "__main__":
    unittest.main()
