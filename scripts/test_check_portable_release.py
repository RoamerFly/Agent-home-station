#!/usr/bin/env python3

import hashlib
import json
import tempfile
import unittest
from pathlib import Path

import check_portable_release


class PortableReleaseCheckerTests(unittest.TestCase):
    def make_release(self, entrypoint="runtime/main.py"):
        root = Path(tempfile.mkdtemp(prefix="portable-release-check-"))
        for name in ("applications", "publictools", "env", "data"):
            (root / name).mkdir()
        (root / "AgentHomeStation.exe").write_bytes(b"synthetic")
        tool = root / "publictools" / "demo-tool"
        (tool / "runtime").mkdir(parents=True)
        (tool / "runtime" / "main.py").write_text("print('ok')\n", encoding="utf-8")
        runtime_digest = hashlib.sha256((tool / "runtime" / "main.py").read_bytes()).hexdigest()
        (tool / "plugin.json").write_text(
            json.dumps(
                {
                    "id": "demo-tool",
                    "name": "Demo Tool",
                    "version": "1",
                    "status": "ready",
                    "runtime": {"entrypoint": entrypoint},
                    "files": {"runtime/main.py": runtime_digest},
                }
            ),
            encoding="utf-8",
        )
        return root

    def test_valid_package_passes(self):
        report = check_portable_release.check_release(self.make_release())
        self.assertTrue(report["ok"], report["errors"])
        self.assertEqual(report["summary"]["public_tools"], 1)

    def test_absolute_entrypoint_is_rejected(self):
        report = check_portable_release.check_release(self.make_release(entrypoint="E:/outside/main.py"))
        self.assertFalse(report["ok"])
        self.assertTrue(any(item["code"] == "missing_or_absolute_entrypoint" for item in report["errors"]))


if __name__ == "__main__":
    unittest.main()
