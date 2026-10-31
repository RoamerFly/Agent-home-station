"""Regression checks for the read-only plugin boundary inventory."""
import importlib.util
import json
import subprocess
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("audit_plugin_boundaries", ROOT / "scripts" / "audit_plugin_boundaries.py")
AUDIT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(AUDIT)


class BoundaryAuditTest(unittest.TestCase):
    def test_categories_cover_control_plane_surfaces(self):
        self.assertEqual(AUDIT.category(Path("agent_tools.go")), "tool")
        self.assertEqual(AUDIT.category(Path("channels_settings.go")), "configuration")
        self.assertEqual(AUDIT.category(Path("agent_workspace.go")), "runtime")
        self.assertEqual(AUDIT.category(Path("capabilities.go")), "prompt")
        self.assertEqual(AUDIT.category(Path("mobile.go")), "route")

    def test_inventory_reports_all_declared_plugins_and_safe_boundary(self):
        output = subprocess.check_output([sys.executable, "scripts/audit_plugin_boundaries.py", "--compact"], cwd=ROOT, text=True)
        report = json.loads(output)
        self.assertTrue(report["audit_only"])
        self.assertEqual(len(report["plugins"]), 9)
        self.assertIn("native_wechat_login_and_send", report["core_boundary"]["retained"])
        self.assertNotIn("account_data", " ".join(str(row) for rows in report["plugins"].values() for row in rows["core_references"]))
        self.assertFalse(report["light_core_ready"])
        self.assertGreater(report["totals"]["core_optional_source_files"], 0)

    def test_static_plugin_implementation_dependency_is_distinct_from_catalog(self):
        dependencies = {
            "wechat-bot/plugins",
            "wechat-bot/plugins/mobile-control/backend",
            "wechat-bot/plugins/share-reader/runtime",
            "wechat-bot/internal/admin",
        }
        self.assertEqual(AUDIT.static_plugin_implementation_dependencies(dependencies), [
            {"package": "wechat-bot/plugins/mobile-control/backend", "target_plugin": "mobile-control"},
            {"package": "wechat-bot/plugins/share-reader/runtime", "target_plugin": "share-reader"},
        ])

    def test_current_go_graph_has_no_static_plugin_implementation_dependency(self):
        output = subprocess.check_output([sys.executable, "scripts/audit_plugin_boundaries.py", "--compact"], cwd=ROOT, text=True)
        report = json.loads(output)
        self.assertEqual(report["static_plugin_implementation_dependencies"], [])
        self.assertEqual(report["totals"]["core_static_plugin_implementation_packages"], 0)

    def test_final_gate_fails_while_inventory_is_incomplete(self):
        result = subprocess.run([sys.executable, "scripts/audit_plugin_boundaries.py", "--require-light"], cwd=ROOT, capture_output=True, text=True)
        self.assertEqual(result.returncode, 1)
        self.assertFalse(json.loads(result.stdout)["light_core_ready"])


if __name__ == "__main__":
    unittest.main()
