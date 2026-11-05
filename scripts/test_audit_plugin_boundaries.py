"""Synthetic boundary mutations plus one honest audit of the current graph."""
import hashlib
import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("audit_plugin_boundaries", ROOT / "scripts/audit_plugin_boundaries.py")
AUDIT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(AUDIT)


class BoundaryAuditTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.packages = []
        for name in ("wechat-admin", "wechat-bot"):
            directory = self.root / "cmd" / name
            directory.mkdir(parents=True)
            (directory / "main.go").write_text("package main\n", encoding="utf8")
            self.packages.append({"ImportPath": "wechat-bot/cmd/" + name, "Dir": str(directory), "GoFiles": ["main.go"]})
        for identity, rules in AUDIT.CAPABILITIES.items():
            ready = any(rule[0] for rule in rules.values())
            caps = [self.cap(name, rule[0], sorted(rule[1])) for name, rule in rules.items()]
            self.write_package(identity, "ready" if ready else "migration-pending", caps, sorted(AUDIT.PACKAGE_PERMISSIONS[identity]))

    @staticmethod
    def cap(name, ready, permissions):
        return {"id": name, "implementation": "ready" if ready else "migration-pending", "description": "Synthetic capability", "discoverable": ready, "permissions": permissions, "input_schema": {"type": "object"}, "output_schema": {"type": "object"}}

    def write_package(self, name, status, caps, permissions, bound=None):
        directory = self.root / "plugins" / name
        directory.mkdir(parents=True, exist_ok=True)
        for filename in AUDIT.REQUIRED_FILES - {"capabilities.json"}:
            (directory / filename).write_text("{}\n" if filename.endswith(".json") else "synthetic fixture\n", encoding="utf8")
        (directory / "capabilities.json").write_text(json.dumps({"version": 1, "capabilities": caps}), encoding="utf8")
        bound = status == "ready" if bound is None else bound
        runtime = {"transport": "unbound"}
        if bound:
            runtime = {"transport": "mcp-stdio", "entrypoint": "runtime/main.py"}
            (directory / "runtime").mkdir(exist_ok=True)
            (directory / "runtime/main.py").write_text("def main():\n    return {}\n", encoding="utf8")
        files = {p.relative_to(directory).as_posix(): hashlib.sha256(p.read_bytes()).hexdigest() for p in directory.rglob("*") if p.is_file() and p.name != "plugin.json"}
        package = {"schema_version": 1, "host_protocol": 1, "id": name, "name": "Synthetic " + name, "version": "1.0.0", "status": status, "permissions": permissions, "runtime": runtime, "platforms": ["windows-amd64", "linux-amd64"], "files": files}
        (directory / "plugin.json").write_text(json.dumps(package), encoding="utf8")
        return directory

    def source(self, text, selected=True):
        directory = self.root / "internal/admin"
        directory.mkdir(parents=True, exist_ok=True)
        filename = "selected.go" if selected else "not_selected.go"
        (directory / filename).write_text(text, encoding="utf8")
        if selected: self.packages.append({"ImportPath": "wechat-bot/internal/admin", "Dir": str(directory), "GoFiles": [filename]})

    def report(self):
        return AUDIT.build_report(self.root, self.packages)

    def reasons(self):
        return {row.get("reason", row["kind"]) for row in self.report()["gate_blockers"]}

    def test_categories_preserve_inventory(self):
        self.assertEqual(AUDIT.category(Path("agent_tools.go")), "tool")
        self.assertEqual(AUDIT.category(Path("channels_settings.go")), "configuration")
        self.assertEqual(AUDIT.category(Path("agent_workspace.go")), "runtime")
        self.assertEqual(AUDIT.category(Path("capabilities.go")), "prompt")
        self.assertEqual(AUDIT.category(Path("mobile.go")), "route")

    def test_service_names_and_core_mediation_are_inventory_only(self):
        self.source('''package admin
// speech_synthesize music_local_start mobileRoutes agenthost video_inspect
var description = "mcptools.ReadPublicShare(ctx, raw, 1, false)"
func resolveReceivedChannels() { broker.InvokeHostService(ctx, "wechat/channels_bridge", args) }
''')
        report = self.report()
        self.assertTrue(report["light_core_ready"], report["gate_blockers"])
        self.assertGreater(report["totals"]["core_optional_source_files"], 0)
        self.assertTrue(all(row["inventory_only"] for row in report["plugins"]["speech"]["core_references"]))

    def test_agenthost_is_retained_as_explicit_core_authority(self):
        self.packages.append({"ImportPath": "wechat-bot/internal/agenthost", "GoFiles": []})
        report = self.report()
        self.assertTrue(report["light_core_ready"])
        self.assertEqual(report["direct_optional_dependencies"], [])
        self.assertIn("isolation", report["retained_dependency_authorities"][0]["reason"])

    def test_old_business_dependencies_fail_even_without_name_markers(self):
        for dependency in ("wechat-bot/internal/bailian", "wechat-bot/internal/mobile", "wechat-bot/internal/channels", "wechat-bot/internal/project/dev"):
            with self.subTest(dependency=dependency):
                report = AUDIT.build_report(self.root, self.packages + [{"ImportPath": dependency, "GoFiles": []}])
                self.assertFalse(report["light_core_ready"])
                self.assertTrue(report["direct_optional_dependencies"])

    def test_static_plugin_implementation_is_distinct_from_catalog(self):
        deps = {"wechat-bot/plugins", "wechat-bot/plugins/mobile-control/backend", "wechat-bot/plugins/speech/dev/legacy-sdk"}
        rows = AUDIT.static_plugin_implementation_dependencies(deps)
        self.assertEqual([row["package"] for row in rows], sorted(deps - {"wechat-bot/plugins"}))
        report = AUDIT.build_report(self.root, self.packages + [{"ImportPath": path, "GoFiles": []} for path in deps])
        self.assertFalse(report["light_core_ready"])
        self.assertEqual(len(report["static_plugin_implementation_dependencies"]), 2)

    def test_real_legacy_resolver_call_fails_with_aliased_import(self):
        self.source('''package admin
import fetch "wechat-bot/internal/mcptools"
func resolve() { fetch.ReadPublicShare(ctx, raw, 42, false) }
''')
        report = self.report()
        self.assertFalse(report["light_core_ready"])
        self.assertEqual(report["source_execution_violations"][0]["symbol"], "ReadPublicShare")
        self.assertEqual(report["source_execution_violations"][0]["line"], 3)

    def test_inline_provider_http_fails_for_literal_and_constant(self):
        for body in ('http.Post("https://dashscope.aliyuncs.com/api/v1/services/audio/tts/SpeechSynthesizer", "application/json", nil)', 'http.Post(endpoint, "application/json", nil)'):
            with self.subTest(body=body):
                self.source('package admin\nimport "net/http"\nconst endpoint = "https://provider.invalid/v1/images/generations"\nfunc run(){ ' + body + ' }\n')
                self.assertIn("inline_optional_provider_http", self.reasons())
                self.packages.pop()

    def test_provider_metadata_without_execution_does_not_fail(self):
        self.source('package admin\nimport "net/http"\nconst endpoint = "https://provider.invalid/v1/images/generations"\n// http.Post(endpoint, "application/json", nil)\n')
        self.assertTrue(self.report()["light_core_ready"])

    def test_unselected_go_file_does_not_become_a_compiled_blocker(self):
        self.source('package admin\nimport "net/http"\nfunc run(){http.Post("https://provider.invalid/v1/images/generations", "application/json", nil)}\n', selected=False)
        self.assertTrue(self.report()["light_core_ready"])

    def test_hidden_unbound_visual_template_is_allowed_without_claiming_ready(self):
        self.write_package("visual-generation", "migration-pending", [self.cap("visual.generate", False, ["network", "artifact.write"])], ["network", "artifact.write"])
        report = self.report()
        self.assertTrue(report["light_core_ready"], report["gate_blockers"])
        self.assertEqual(report["plugins"]["visual-generation"]["available_capabilities"], 0)
        self.assertEqual(report["totals"]["ready_plugins"], sum(any(rule[0] for rule in rules.values()) for rules in AUDIT.CAPABILITIES.values()))

    def test_illegal_template_visibility_binding_or_permission_fails(self):
        for change in ("visible", "bound", "permission"):
            with self.subTest(change=change):
                cap = self.cap("visual.generate", False, ["network", "artifact.write"])
                permissions = ["network", "artifact.write"]
                if change == "visible": cap["discoverable"] = True
                if change == "permission": cap["permissions"].append("process.execute"); permissions.append("process.execute")
                self.write_package("visual-generation", "template", [cap], permissions, bound=change == "bound")
                self.assertFalse(self.report()["light_core_ready"])

    def test_pending_capability_cannot_be_relabelled_ready(self):
        self.write_package("visual-generation", "ready", [self.cap("visual.generate", True, ["network", "artifact.write"])], ["network", "artifact.write"])
        self.assertIn("unreviewed_or_unbound_ready_capability", self.reasons())

    def test_new_unknown_capability_requires_review(self):
        self.write_package("speech", "ready", [self.cap("speech.synthesize", True, ["model.invoke"]), self.cap("speech.unreviewed", True, ["model.invoke"])], ["model.invoke"])
        self.assertIn("unreviewed_capability", self.reasons())

    def test_ready_runtime_missing_and_capability_overgrant_fail(self):
        directory = self.root / "plugins/speech"
        (directory / "runtime/main.py").unlink()
        self.assertIn("ready_runtime_unavailable", self.reasons())
        self.write_package("speech", "ready", [self.cap("speech.synthesize", True, ["model.invoke", "device"])], ["model.invoke", "device"])
        self.assertIn("capability_permission_mismatch", self.reasons())

    def test_integrity_is_dynamic_manifest_validation_not_a_source_baseline(self):
        directory = self.root / "plugins/speech"
        (directory / "runtime/main.py").write_text("# changed synthetic implementation\ndef main(): return {}\n", encoding="utf8")
        self.assertIn("package_file_digest_mismatch", self.reasons())
        package = json.loads((directory / "plugin.json").read_text())
        package["files"]["runtime/main.py"] = hashlib.sha256((directory / "runtime/main.py").read_bytes()).hexdigest()
        (directory / "plugin.json").write_text(json.dumps(package), encoding="utf8")
        self.assertTrue(self.report()["light_core_ready"])

    def test_each_reviewed_catalog_plugin_is_required_including_pending_template(self):
        for identity in ("speech", "visual-generation"):
            with self.subTest(identity=identity):
                manifest = self.root / "plugins" / identity / "plugin.json"
                original = manifest.read_bytes()
                manifest.unlink()
                blockers = self.report()["gate_blockers"]
                self.assertTrue(any(row.get("reason") == "required_catalog_plugin_missing" and row.get("path") == "plugins/" + identity for row in blockers))
                manifest.write_bytes(original)
        self.assertTrue(self.report()["light_core_ready"])

    def test_ready_package_requires_every_reviewed_ready_capability(self):
        for missing in (True, False):
            with self.subTest(missing=missing):
                caps = [self.cap("speech.synthesize", True, ["model.invoke"])]
                if not missing:
                    caps.append(self.cap("speech.synthesize_named", False, ["artifact.read", "model.invoke"]))
                self.write_package("speech", "ready", caps, ["artifact.read", "model.invoke"])
                blockers = self.report()["gate_blockers"]
                self.assertTrue(any(row.get("reason") == "required_ready_capability_missing" and row.get("capability") == "speech.synthesize_named" for row in blockers))

    def test_malformed_identity_and_missing_manifest_fail_without_crashing(self):
        manifest = self.root / "plugins/speech/plugin.json"
        package = json.loads(manifest.read_text())
        package["id"] = ["speech"]
        manifest.write_text(json.dumps(package), encoding="utf8")
        self.assertIn("invalid_package_or_capability_document", self.reasons())
        manifest.unlink()
        self.assertIn("package_manifest_missing", self.reasons())

    def test_package_file_paths_cannot_escape_with_a_windows_drive(self):
        self.assertFalse(AUDIT.safe_relative("C:/outside.py"))
        self.assertFalse(AUDIT.safe_relative("../outside.py"))
        self.assertTrue(AUDIT.safe_relative("runtime/main.py"))

    def test_current_go_graph_report_has_consistent_exit_and_explicit_limits(self):
        result = subprocess.run([sys.executable, "-B", "scripts/audit_plugin_boundaries.py", "--require-light", "--compact"], cwd=ROOT, capture_output=True, text=True, encoding="utf8")
        report = json.loads(result.stdout)
        self.assertEqual(report["light_core_ready"], not report["gate_blockers"])
        self.assertEqual(result.returncode, 0 if report["light_core_ready"] else 1)
        self.assertTrue(report["audit_only"])
        self.assertEqual(report["build_roots"], list(AUDIT.BUILD_ROOTS))
        self.assertTrue(report["limitations"])
        self.assertEqual(set(report["plugins"]), {path.parent.name for path in (ROOT / "plugins").glob("*/plugin.json")})
        self.assertIn("native_wechat_login_and_send", report["core_boundary"]["retained"])


if __name__ == "__main__":
    unittest.main()
