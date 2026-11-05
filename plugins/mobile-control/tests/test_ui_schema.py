"""Declarative mobile admin UI contract; no browser, device, or host process."""
import json
from pathlib import Path
import unittest


PACKAGE = Path(__file__).resolve().parents[1]


class UISchemaTests(unittest.TestCase):
    def test_only_host_supported_sections_and_fixed_host_bindings(self):
        schema = json.loads((PACKAGE / "ui.schema.json").read_text(encoding="utf-8"))
        self.assertEqual(schema["version"], 1)
        self.assertEqual(
            schema["sections"],
            [
                {"id": "configuration", "title": "执行设置与已登记连接", "component": "configuration", "binding": "host_data.settings", "action": "settings.save"},
                {"id": "tasks", "title": "设备任务", "component": "tasks", "binding": "host_data.tasks", "action": "tasks.create"},
                {"id": "resources", "title": "任务素材", "component": "resources", "binding": "host_data.resources"},
                {"id": "diagnostics", "title": "状态与说明", "component": "diagnostics", "binding": "host_data.diagnostics", "action": "devices.list"},
            ],
        )
        self.assertEqual(set(schema), {"version", "sections"})

    def test_generic_plugin_configuration_remains_closed(self):
        configuration = json.loads((PACKAGE / "config.schema.json").read_text(encoding="utf-8"))
        self.assertEqual(configuration["properties"], {})
        self.assertFalse(configuration["additionalProperties"])


if __name__ == "__main__":
    unittest.main()
