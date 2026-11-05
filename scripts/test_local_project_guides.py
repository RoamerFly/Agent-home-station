import json
from pathlib import Path
import tempfile
import unittest

from local_project_guides import DOCKER_NOTE, LOCAL_NOTE, refresh_execution_notes


class LocalProjectGuideTests(unittest.TestCase):
    def test_only_builtin_note_changes_once_on_wsl(self):
        with tempfile.TemporaryDirectory() as tmp:
            directory = Path(tmp) / "public_workspace/manifests"
            directory.mkdir(parents=True)
            stock = {"execution": DOCKER_NOTE, "root": "/managed/rvc", "usage": "保留私人备注"}
            custom = {"execution": "My own service", "root": "/managed/yue"}
            (directory / "rvc.json").write_text(json.dumps(stock), encoding="utf-8-sig")
            (directory / "yue.json").write_text(json.dumps(custom), encoding="utf-8")
            before = (directory / "yue.json").read_bytes()
            self.assertEqual(refresh_execution_notes(tmp, "docker"), 0)
            self.assertEqual(refresh_execution_notes(tmp, "wsl"), 1)
            result = json.loads((directory / "rvc.json").read_text(encoding="utf-8"))
            self.assertEqual(result, {**stock, "execution": LOCAL_NOTE})
            self.assertEqual((directory / "yue.json").read_bytes(), before)
            self.assertEqual(refresh_execution_notes(tmp, "wsl"), 0)

    def test_linked_manifests_are_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            directory = root / "public_workspace/manifests"
            directory.mkdir(parents=True)
            outside = root / "outside.json"
            outside.write_text(json.dumps({"execution": DOCKER_NOTE}))
            try:
                (directory / "rvc.json").symlink_to(outside)
            except OSError:
                self.skipTest("symlink creation unavailable")
            with self.assertRaisesRegex(ValueError, "project_guide_is_link"):
                refresh_execution_notes(tmp, "wsl")
            self.assertEqual(json.loads(outside.read_text())["execution"], DOCKER_NOTE)


if __name__ == "__main__":
    unittest.main()
