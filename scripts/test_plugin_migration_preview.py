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
        for plugin, status in (("speech", "ready"), ("media-understanding", "ready"), ("web-search", "ready"), ("share-reader", "migration-pending")):
            directory = repo / "plugins" / plugin
            directory.mkdir(parents=True)
            (directory / "plugin.json").write_text(json.dumps({"id": plugin, "version": "1.2.3", "status": status}), encoding="utf8")
        return repo

    def make_root(self, base):
        root = base / "admin"
        root.mkdir()
        secret = "synthetic-secret-never-report"
        (root / "config.json").write_text(json.dumps({"provider": "deepseek", "base_url": "https://api.deepseek.com", "api_key": secret, "model": "vision-a", "max_tokens": 900, "thinking_enabled": True, "reasoning_effort": "high"}), encoding="utf8")
        (root / "speech-config.json").write_text(json.dumps({"endpoint": "https://dashscope.aliyuncs.com", "key": secret, "tts_enabled": True, "tts_model": "tts-a", "voice": "voice-a", "instruction": "calm", "asr_enabled": True, "asr_backend": "local", "asr_local_model": "sensevoice-test"}), encoding="utf8")
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
            media = next(x for x in main["plugins"] if x["plugin"] == "media-understanding")
            self.assertIn("registered_local_asr_profile:sensevoice-test", media["required_missing"])
            speech = next(x for x in main["plugins"] if x["plugin"] == "speech")
            self.assertEqual(speech["values"]["tts_endpoint"], "https://dashscope.aliyuncs.com/api/v1/services/audio/tts/SpeechSynthesizer")
            web = next(x for x in main["plugins"] if x["plugin"] == "web-search")
            self.assertIn("official_deepseek_search_adapter_not_declared_by_web-search_plugin", web["required_missing"])
            self.assertFalse(web["proposed_enabled"])
            self.assertEqual(handoff["accounts"][0]["secrets"]["speech.tts_key"], secret)
            self.assertEqual(before, {p.relative_to(root): p.read_bytes() for p in root.rglob("*") if p.is_file()})

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
