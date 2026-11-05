"""Source layout/link checks plus native Windows launcher portability checks."""
from pathlib import Path
import os
import re
import subprocess
import tempfile
import unittest
import json
import shutil

ROOT = Path(__file__).resolve().parents[1]


class StructureTests(unittest.TestCase):
    def test_markdown_targets_survive_relocation(self):
        skipped = {"public_workspace", "pri_workspace", ".git", ".agents", "temp", "secrets", "dist", ".playwright-cli"}
        missing = []
        markdown=[]
        for directory, dirs, names in os.walk(ROOT):
            dirs[:]=[name for name in dirs if name not in skipped]
            markdown.extend(Path(directory)/name for name in names if name.endswith(".md"))
        for file in markdown:
            for target in re.findall(r"\]\(([^)]+)\)", file.read_text(encoding="utf-8-sig", errors="replace")):
                target = target.strip("<>").split("#", 1)[0]
                if not target or re.match(r"^[a-zA-Z][a-zA-Z0-9+.-]*:", target) or target.startswith("/"):
                    continue
                resolved = file.parent / target
                if not resolved.exists() and not {"evidence", "samples", "temp", "dist"}.intersection(resolved.parts):
                    missing.append((str(file.relative_to(ROOT)), target))
        self.assertEqual(missing, [])

    def test_deployment_context_still_points_to_source(self):
        directory = ROOT / "deploy/docker"
        for name in ["compose.yaml", "compose.linux-native.yaml"]:
            text = (directory / name).read_text(encoding="utf-8")
            self.assertIn("context: ../..", text)
            dockerfile = re.search(r"dockerfile:\s*(\S+)", text).group(1)
            self.assertTrue((directory / "../.." / dockerfile).is_file())
        self.assertIn("../../secrets/", (directory / "compose.bot.yaml").read_text(encoding="utf-8"))

    @unittest.skipUnless(os.name == "nt", "native Windows launcher check")
    def test_launcher_resolves_unicode_space_path_from_another_directory(self):
        shell = Path(os.environ["SystemRoot"]) / "System32/WindowsPowerShell/v1.0/powershell.exe"
        with tempfile.TemporaryDirectory(prefix="大肥鱼 launcher ") as temporary:
            root = Path(temporary)
            (root / "scripts").mkdir()
            for name in ["Launch-Local.ps1", "Start-Local-Admin.ps1", "Start-Local-YuE.ps1"]:
                shutil.copyfile(ROOT / "scripts" / name, root / "scripts" / name)
            for action in ["Start", "Web", "Music", "Status"]:
                result = subprocess.run([str(shell), "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", str(root / "scripts/Launch-Local.ps1"), "-Action", action, "-CheckOnly", "-NoDialog"], cwd=os.environ["SystemRoot"], capture_output=True, encoding="utf-8", check=True)
                view = json.loads(result.stdout)
                self.assertEqual(Path(view["workspace"]), root)
                self.assertEqual(view["action"], action)
                self.assertTrue(view["dependencies_valid"])
            self.assertFalse((root / "temp").exists(), "check-only unexpectedly started something")


if __name__ == "__main__":
    unittest.main()
