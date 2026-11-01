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
        for plugin, status in (("speech", "ready"), ("media-understanding", "ready"), ("music", "ready"), ("project-development", "ready"), ("web-search", "ready"), ("share-reader", "migration-pending"), ("external-tools", "ready")):
            directory = repo / "plugins" / plugin
            directory.mkdir(parents=True)
            manifest = {"id": plugin, "version": "1.2.3", "status": status}
            if plugin == "music":
                manifest["permissions"] = ["model.invoke"]
            if plugin == "project-development":
                manifest["permissions"] = ["workspace.read", "workspace.write", "process.execute"]
            if plugin == "external-tools":
                manifest["permissions"] = ["network"]
            (directory / "plugin.json").write_text(json.dumps(manifest), encoding="utf8")
        return repo

    def add_declared_external_sync(self, repo, *, jobs=False):
        external = repo / "plugins" / "external-tools"
        (external / "config.schema.json").write_text(json.dumps({"type": "object", "properties": {}, "additionalProperties": False}), encoding="utf8")
        capabilities = [{
            "id": "external.http_request", "implementation": "ready", "discoverable": True,
            "async": False, "permissions": ["network"],
        }]
        if jobs:
            capabilities.append({"id": "external.job_request", "implementation": "ready", "discoverable": True,
                                 "async": False, "permissions": ["network"]})
        (external / "capabilities.json").write_text(json.dumps({"capabilities": capabilities}), encoding="utf8")

    def write_external_ledger(self, root, *, mode="sync"):
        secret = "external-secret-must-not-appear"
        if mode == "sync":
            manifest = {"id": "legacy-http", "name": "Legacy HTTP", "tools": [{
                "name": "lookup", "url": "https://private.example/v1", "headers": {"Authorization": "Bearer {{secret.TOKEN}}"},
            }]}
        elif mode == "async":
            manifest = {"id": "legacy-async", "name": "Legacy async", "tools": [{
                "name": "render", "url": "https://private.example/jobs", "job": {"id_path": "job_id"}, "poll": {"url": "https://private.example/jobs/{{result.job_id}}"},
            }]}
        elif mode == "poll-only":
            manifest = {"id": "legacy-poll", "name": "Legacy poll only", "tools": [{
                "name": "observe", "url": "https://private.example/jobs", "poll": {"url": "https://private.example/jobs/{{result.job_id}}"},
            }]}
        elif mode == "mcp":
            manifest = {"id": "legacy-mcp", "name": "Legacy MCP", "mcp": {"transport": "http", "url": "https://private.example/mcp"}}
        else:
            raise ValueError(mode)
        (root / "plugins.json").write_text(json.dumps({manifest["id"]: {"manifest": manifest, "enabled": True, "secrets": {"TOKEN": secret}}}), encoding="utf8")
        return secret

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

    def add_declared_named_speech(self, repo):
        speech = repo / "plugins" / "speech"
        manifest = json.loads((speech / "plugin.json").read_text(encoding="utf8"))
        manifest["permissions"] = ["artifact.read", "model.invoke"]
        (speech / "plugin.json").write_text(json.dumps(manifest), encoding="utf8")
        (speech / "config.schema.json").write_text(json.dumps({"type": "object", "properties": {"local_profile_id": {"type": "string", "enum": ["", "gpt-sovits"]}}}), encoding="utf8")
        (speech / "capabilities.json").write_text(json.dumps({"capabilities": [{"id": "speech.synthesize_named", "implementation": "ready", "permissions": ["artifact.read", "model.invoke"]}]}), encoding="utf8")

    def add_declared_local_music_bridge(self, repo):
        music = repo / "plugins" / "music"
        manifest = json.loads((music / "plugin.json").read_text(encoding="utf8"))
        manifest["permissions"] = ["model.invoke", "network"]
        (music / "plugin.json").write_text(json.dumps(manifest), encoding="utf8")
        (music / "config.schema.json").write_text(json.dumps({"type": "object", "properties": {
            "local_endpoint": {"type": "string"},
            "local_token": {"type": "string", "x-secret": True, "x-host-only": True},
        }, "additionalProperties": False}), encoding="utf8")
        (music / "capabilities.json").write_text(json.dumps({"capabilities": [{"id": "music.bridge_request", "implementation": "ready", "permissions": ["network"]}]}), encoding="utf8")

    def add_pending_named_speech_manifest(self, root):
        manifest = root / "managed-workspaces" / "public_workspace" / "manifests" / "gpt-sovits.json"
        manifest.parent.mkdir(parents=True)
        manifest.write_text(json.dumps({"id": "gpt-sovits", "root": "/not-disclosed"}), encoding="utf8")

    def add_declared_project_development(self, repo):
        project = repo / "plugins" / "project-development"
        (project / "config.schema.json").write_text(json.dumps({"type": "object", "properties": {}, "additionalProperties": False}), encoding="utf8")
        (project / "capabilities.json").write_text(json.dumps({"capabilities": [
            {"id": "project.workspace_read", "implementation": "ready", "permissions": ["workspace.read"]},
            {"id": "project.workspace_write", "implementation": "ready", "permissions": ["workspace.write"]},
            {"id": "project.workspace_execute", "implementation": "ready", "permissions": ["process.execute"]},
        ]}), encoding="utf8")

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
            self.assertEqual([step["plugin"] for step in main["plugins"]], ["media-understanding", "speech", "music", "project-development", "web-search", "share-reader", "external-tools"])
            self.assertIn("plugins_json", main["source_hashes"])
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

    def test_project_development_preserves_full_intent_without_copying_host_scope(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base)
            self.add_declared_project_development(repo)
            root, _ = self.make_root(base)
            private_root = "E:\\private-project-root"
            (root / "agent-config.json").write_text(json.dumps({"enabled": True, "host": {
                "mode": "full", "roots": [private_root], "isolation": {"private_profile": "do-not-copy"},
                "windows": {"helper": "do-not-copy"}, "timeout_seconds": 90, "output_bytes": 32000,
            }}), encoding="utf8")
            before = {path.relative_to(root): path.read_bytes() for path in root.rglob("*") if path.is_file()}
            report, handoff = preview.build_preview(root, repo, "main")
            project = next(step for step in report["accounts"][0]["plugins"] if step["plugin"] == "project-development")
            self.assertEqual(project["status"], "ready_for_administrator_review")
            self.assertTrue(project["legacy_enabled"])
            self.assertTrue(project["eligible_to_enable_after_confirmation"])
            self.assertFalse(project["proposed_enabled"])
            self.assertEqual(project["values"], {})
            self.assertEqual(project["secrets"], {})
            self.assertEqual(set(project["permissions"]), {"workspace.read", "workspace.write", "process.execute"})
            self.assertIn("agent-config.json:host.roots", project["unmapped_legacy_fields"])
            public = json.dumps(report, ensure_ascii=False)
            self.assertNotIn(private_root, public)
            self.assertNotIn("do-not-copy", public)
            self.assertNotIn("project-development", handoff["accounts"][0]["secrets"])
            self.assertEqual(before, {path.relative_to(root): path.read_bytes() for path in root.rglob("*") if path.is_file()})

    def test_external_sync_migration_keeps_legacy_ledger_and_credentials_host_retained(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base)
            self.add_declared_external_sync(repo)
            root, _ = self.make_root(base)
            secret = self.write_external_ledger(root, mode="sync")
            before = {path.relative_to(root): path.read_bytes() for path in root.rglob("*") if path.is_file()}
            report, handoff = preview.build_preview(root, repo, "main")
            account = report["accounts"][0]
            external = next(step for step in account["plugins"] if step["plugin"] == "external-tools")
            self.assertEqual(external["status"], "ready_for_administrator_review")
            self.assertTrue(external["legacy_enabled"])
            self.assertTrue(external["eligible_to_enable_after_confirmation"])
            self.assertFalse(external["proposed_enabled"])
            self.assertEqual(external["values"], {})
            self.assertEqual(external["secrets"], {})
            self.assertEqual(external["permissions"], ["network"])
            self.assertEqual(external["required_missing"], [])
            self.assertIn("plugins.json:host_retained_declarations_and_credentials", external["unmapped_legacy_fields"])
            self.assertIn("plugins_json", account["source_hashes"])
            public = json.dumps(report, ensure_ascii=False)
            self.assertNotIn(secret, public)
            self.assertNotIn("private.example", public)
            self.assertFalse(any(key.startswith("external-tools.") for key in handoff["accounts"][0]["secrets"]))
            self.assertEqual(before, {path.relative_to(root): path.read_bytes() for path in root.rglob("*") if path.is_file()})

    def test_external_job_request_migrates_empty_configuration_but_requires_task_proof_review(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base)
            self.add_declared_external_sync(repo, jobs=True)
            root, _ = self.make_root(base)
            secret = self.write_external_ledger(root, mode="async")
            report, handoff = preview.build_preview(root, repo, "main")
            external = next(step for step in report["accounts"][0]["plugins"] if step["plugin"] == "external-tools")
            self.assertEqual(external["status"], "ready_for_administrator_review")
            self.assertTrue(external["legacy_enabled"])
            self.assertTrue(external["eligible_to_enable_after_confirmation"])
            self.assertEqual(external["values"], {})
            self.assertEqual(external["secrets"], {})
            self.assertIn("legacy_external_tasks:origin_and_configuration_proof_required", external["unmapped_legacy_fields"])
            self.assertNotIn(secret, json.dumps(report, ensure_ascii=False))
            self.assertFalse(any(key.startswith("external-tools.") for key in handoff["accounts"][0]["secrets"]))

    def test_external_mcp_or_poll_only_is_blocked_without_provider_substitution(self):
        for mode in ("mcp", "poll-only"):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as temporary:
                base = Path(temporary)
                repo = self.make_repo(base)
                self.add_declared_external_sync(repo)
                root, _ = self.make_root(base)
                secret = self.write_external_ledger(root, mode=mode)
                report, handoff = preview.build_preview(root, repo, "main")
                external = next(step for step in report["accounts"][0]["plugins"] if step["plugin"] == "external-tools")
                self.assertEqual(external["status"], "blocked")
                self.assertFalse(external["legacy_enabled"])
                self.assertFalse(external["eligible_to_enable_after_confirmation"])
                self.assertFalse(external["proposed_enabled"])
                self.assertEqual(external["values"], {})
                self.assertEqual(external["secrets"], {})
                self.assertIn("no_matching_supported_external_tools", external["required_missing"])
                self.assertNotIn(secret, json.dumps(report, ensure_ascii=False))
                self.assertFalse(any(key.startswith("external-tools.") for key in handoff["accounts"][0]["secrets"]))

    def test_external_sync_requires_reviewed_adapter_contract(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base)
            root, _ = self.make_root(base)
            self.write_external_ledger(root, mode="sync")
            report, _ = preview.build_preview(root, repo, "main")
            external = next(step for step in report["accounts"][0]["plugins"] if step["plugin"] == "external-tools")
            self.assertEqual(external["status"], "blocked")
            self.assertIn("official_sync_external_adapter_not_declared_by_external_tools_plugin", external["required_missing"])

    def test_external_job_requires_reviewed_job_capability(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base)
            self.add_declared_external_sync(repo)
            root, _ = self.make_root(base)
            self.write_external_ledger(root, mode="async")
            report, _ = preview.build_preview(root, repo, "main")
            external = next(step for step in report["accounts"][0]["plugins"] if step["plugin"] == "external-tools")
            self.assertEqual(external["status"], "blocked")
            self.assertFalse(external["legacy_enabled"])
            self.assertIn("no_matching_supported_external_tools", external["required_missing"])
            self.assertIn("official_external_job_adapter_not_declared_by_external_tools_plugin", external["required_missing"])

    def test_project_development_does_not_expand_read_or_files_scope(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base)
            self.add_declared_project_development(repo)
            root, _ = self.make_root(base)
            path = root / "agent-config.json"
            for mode in ("read", "files"):
                with self.subTest(mode=mode):
                    path.write_text(json.dumps({"enabled": True, "host": {"mode": mode, "roots": ["E:\\private-root"]}}), encoding="utf8")
                    report, _ = preview.build_preview(root, repo, "main")
                    project = next(step for step in report["accounts"][0]["plugins"] if step["plugin"] == "project-development")
                    self.assertEqual(project["status"], "blocked")
                    self.assertFalse(project["legacy_enabled"])
                    self.assertFalse(project["eligible_to_enable_after_confirmation"])
                    self.assertIn("explicit_project_permissions_review_required", project["required_missing"])
                    self.assertEqual(project["values"], {})

    def test_project_development_requires_reviewed_three_capability_contract(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base)
            root, _ = self.make_root(base)
            (root / "agent-config.json").write_text(json.dumps({"enabled": True, "host": {"mode": "full", "roots": ["E:\\private-root"]}}), encoding="utf8")
            report, _ = preview.build_preview(root, repo, "main")
            project = next(step for step in report["accounts"][0]["plugins"] if step["plugin"] == "project-development")
            self.assertEqual(project["status"], "blocked")
            self.assertIn("official_project_development_adapter_not_declared_by_plugin", project["required_missing"])

    def test_real_project_development_contract_preserves_disabled_application(self):
        with tempfile.TemporaryDirectory() as temporary:
            root, _ = self.make_root(Path(temporary))
            (root / "agent-config.json").write_text(json.dumps({"enabled": True, "host": {"mode": "full", "roots": ["E:\\private-root"]}}), encoding="utf8")
            repo = MODULE.resolve().parents[1]
            report, handoff = preview.build_preview(root, repo, "main")
            project = next(step for step in report["accounts"][0]["plugins"] if step["plugin"] == "project-development")
            manifest_raw = (repo / "plugins" / "project-development" / "plugin.json").read_bytes()
            manifest = json.loads(manifest_raw)
            self.assertEqual(project["package"]["version"], manifest["version"])
            self.assertEqual(project["package"]["expected_package_digest"], hashlib.sha256(manifest_raw).hexdigest())
            self.assertEqual(project["status"], "ready_for_administrator_review")
            self.assertTrue(project["legacy_enabled"])
            self.assertFalse(project["proposed_enabled"])
            self.assertEqual(project["values"], {})
            self.assertEqual(project["secrets"], {})
            self.assertFalse(any(key.startswith("project-development.") for key in handoff["accounts"][0]["secrets"]))

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

    def test_named_speech_manifest_requires_host_verification_without_private_data(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base)
            self.add_declared_named_speech(repo)
            root, secret = self.make_root(base)
            self.add_pending_named_speech_manifest(root)
            before = {path.relative_to(root): path.read_bytes() for path in root.rglob("*") if path.is_file()}
            report, handoff = preview.build_preview(root, repo, "main")
            speech = next(step for step in report["accounts"][0]["plugins"] if step["plugin"] == "speech")
            self.assertEqual(speech["values"]["local_profile_id"], "gpt-sovits")
            self.assertEqual(speech["profile_id"], "gpt-sovits")
            self.assertEqual(speech["resource_proof"], {"source": "gpt-sovits-manifest", "present": True, "host_verification": "required"})
            self.assertIn("registered_named_speech_profile:gpt-sovits:host_verification_required", speech["required_missing"])
            self.assertEqual(speech["status"], "blocked")
            self.assertFalse(speech["eligible_to_enable_after_confirmation"])
            self.assertFalse(speech["proposed_enabled"])
            public = json.dumps(report, ensure_ascii=False)
            self.assertNotIn("/not-disclosed", public)
            self.assertNotIn(secret, public)
            self.assertEqual(before, {path.relative_to(root): path.read_bytes() for path in root.rglob("*") if path.is_file()})
            self.assertEqual(handoff["accounts"][0]["secrets"]["speech.tts_key"], secret)

    def test_no_named_manifest_keeps_cloud_only_speech_preview(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base)
            self.add_declared_named_speech(repo)
            root, _ = self.make_root(base)
            report, _ = preview.build_preview(root, repo, "main")
            speech = next(step for step in report["accounts"][0]["plugins"] if step["plugin"] == "speech")
            self.assertNotIn("local_profile_id", speech["values"])
            self.assertNotIn("profile_id", speech)
            self.assertNotIn("resource_proof", speech)
            self.assertNotIn("registered_named_speech_profile:gpt-sovits:host_verification_required", speech["required_missing"])
            self.assertEqual(speech["status"], "ready_for_administrator_review")

    def test_audio_review_maps_host_only_key_without_changing_media_enablement(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base)
            root, secret = self.make_root(base)
            speech_path = root / "speech-config.json"
            speech = json.loads(speech_path.read_text(encoding="utf8"))
            speech["endpoint"] = "https://dashscope-intl.aliyuncs.com/"
            speech_path.write_text(json.dumps(speech), encoding="utf8")
            before = {p.relative_to(root): p.read_bytes() for p in root.rglob("*") if p.is_file()}
            report, handoff = preview.build_preview(root, repo, "main")
            media = next(step for step in report["accounts"][0]["plugins"] if step["plugin"] == "media-understanding")
            self.assertEqual(media["values"]["audio_review_endpoint"], "https://dashscope-intl.aliyuncs.com/compatible-mode/v1/chat/completions")
            self.assertEqual(media["values"]["audio_review_model"], "qwen3.8-omni-flash")
            self.assertEqual(media["secrets"]["audio_review_key"], {"source_field": "speech-config.json:key", "present": True, "sha256": hashlib.sha256(secret.encode()).hexdigest()})
            self.assertNotIn("audio_review_key", media["values"])
            self.assertEqual(handoff["accounts"][0]["secrets"]["media-understanding.audio_review_key"], secret)
            self.assertTrue(media["legacy_enabled"], "the existing ASR state, not the review key, controls media enablement")
            self.assertNotIn(secret, json.dumps(report, ensure_ascii=False))
            self.assertEqual(before, {p.relative_to(root): p.read_bytes() for p in root.rglob("*") if p.is_file()})

    def test_empty_audio_review_key_omits_optional_listening_configuration(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base)
            root, _ = self.make_root(base)
            speech_path = root / "speech-config.json"
            speech = json.loads(speech_path.read_text(encoding="utf8"))
            speech["key"] = ""
            speech["asr_enabled"] = False
            speech_path.write_text(json.dumps(speech), encoding="utf8")
            report, handoff = preview.build_preview(root, repo, "main")
            media = next(step for step in report["accounts"][0]["plugins"] if step["plugin"] == "media-understanding")
            self.assertNotIn("audio_review_endpoint", media["values"])
            self.assertNotIn("audio_review_model", media["values"])
            self.assertNotIn("audio_review_key", media["secrets"])
            self.assertNotIn("media-understanding.audio_review_key", handoff["accounts"][0]["secrets"])
            self.assertNotIn("audio_review_key", media["required_missing"])

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
            for field in ("tts_*", "asr_*", "named_voice_library", "rvc_profiles"):
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
            manifest_raw = (repo / "plugins" / "music" / "plugin.json").read_bytes()
            manifest = json.loads(manifest_raw)
            digest = hashlib.sha256(manifest_raw).hexdigest()
            self.assertEqual(music["package"]["expected_package_digest"], digest)
            self.assertEqual(music["package"]["version"], manifest["version"])
            self.assertEqual(music["package"]["status"], "ready")
            self.assertEqual(music["permissions"], manifest["permissions"])
            self.assertIs(music["proposed_enabled"], False)
            self.assertNotIn(secret, json.dumps(report, ensure_ascii=False))
            self.assertEqual(handoff["accounts"][0]["secrets"]["music.music_key"], secret)

    def test_local_music_bridge_maps_without_cloud_key_and_redacts_local_token(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base)
            self.add_declared_local_music_bridge(repo)
            root, _ = self.make_root(base)
            path = root / "speech-config.json"
            speech = json.loads(path.read_text(encoding="utf8"))
            speech.update({"music_enabled": True, "key": "", "yue_enabled": True, "yue_endpoint": "http://127.0.0.1:8765/", "yue_token": "synthetic-local-token"})
            path.write_text(json.dumps(speech), encoding="utf8")
            report, handoff = preview.build_preview(root, repo, "main")
            music = next(step for step in report["accounts"][0]["plugins"] if step["plugin"] == "music")
            self.assertEqual(music["status"], "ready_for_administrator_review")
            self.assertTrue(music["legacy_enabled"])
            self.assertTrue(music["eligible_to_enable_after_confirmation"])
            self.assertFalse(music["proposed_enabled"])
            self.assertEqual(music["values"]["local_endpoint"], "http://127.0.0.1:8765")
            self.assertEqual(music["secrets"]["local_token"], {"source_field": "speech-config.json:yue_token", "present": True, "sha256": hashlib.sha256(b"synthetic-local-token").hexdigest()})
            self.assertEqual(handoff["accounts"][0]["secrets"]["music.local_token"], "synthetic-local-token")
            self.assertNotIn("music_key", music["required_missing"])
            self.assertNotIn("synthetic-local-token", json.dumps(report, ensure_ascii=False))

    def test_local_music_bridge_requires_declared_safe_contract(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base)
            root, _ = self.make_root(base)
            path = root / "speech-config.json"
            speech = json.loads(path.read_text(encoding="utf8"))
            speech.update({"yue_enabled": True, "yue_endpoint": "http://127.0.0.1:8765", "yue_token": "synthetic-local-token"})
            path.write_text(json.dumps(speech), encoding="utf8")
            report, handoff = preview.build_preview(root, repo, "main")
            music = next(step for step in report["accounts"][0]["plugins"] if step["plugin"] == "music")
            self.assertEqual(music["status"], "blocked")
            self.assertIn("official_local_music_bridge_not_declared_by_music_plugin", music["required_missing"])
            self.assertNotIn("local_endpoint", music["values"])
            self.assertNotIn("music.local_token", handoff["accounts"][0]["secrets"])

            self.add_declared_local_music_bridge(repo)
            speech["yue_endpoint"] = "http://music.example"
            path.write_text(json.dumps(speech), encoding="utf8")
            report, handoff = preview.build_preview(root, repo, "main")
            music = next(step for step in report["accounts"][0]["plugins"] if step["plugin"] == "music")
            self.assertEqual(music["status"], "blocked")
            self.assertIn("local_endpoint_invalid", music["required_missing"])
            self.assertNotIn("local_endpoint", music["values"])
            self.assertNotIn("music.local_token", handoff["accounts"][0]["secrets"])

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
