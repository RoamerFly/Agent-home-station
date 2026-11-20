"""Synthetic checks for program replacement and private data preservation."""

import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from update_windows_release import EXE, SERVER, Replacement, payload


class UpdateTests(unittest.TestCase):
    def test_payload_updates_indexed_package_files_only(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            candidate, release = root / "candidate", root / "release"
            staged = candidate / "publictools" / "tool"
            installed = release / "publictools" / "tool-local"
            for package in (staged, installed):
                package.mkdir(parents=True)
                (package / "plugin.json").write_text(
                    json.dumps({"id": "tool", "files": {"run.py": "synthetic"}}),
                    encoding="utf-8",
                )
            (staged / "run.py").write_bytes(b"new")
            (staged / "models").mkdir()
            (staged / "models" / "weight").write_bytes(b"candidate model")
            files = payload(candidate, release)
            self.assertEqual(set(files), {
                Path(EXE), SERVER, installed.relative_to(release) / "plugin.json",
                installed.relative_to(release) / "run.py",
            })

    def test_replace_then_restore_preserves_data_and_removes_new_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            release = root / "release"
            data = release / "data"
            data.mkdir(parents=True)
            canary = data / "keep.txt"
            canary.write_bytes(b"private fixture")
            (release / EXE).write_bytes(b"old")
            source = root / "source"
            source.write_bytes(b"candidate")
            transaction = Replacement(release, root / "transaction")
            transaction.install(Path(EXE), source)
            transaction.install(Path("env/tool.exe"), source)
            self.assertEqual((release / EXE).read_bytes(), b"candidate")
            locked = release / "env/locked.exe"
            locked.write_bytes(b"unchanged")
            replace = os.replace

            def locked_target(source, target):
                if target == locked:
                    raise PermissionError("injected lock")
                replace(source, target)

            with patch("update_windows_release.os.replace", side_effect=locked_target):
                with self.assertRaises(PermissionError):
                    transaction.install(Path("env/locked.exe"), source)
            transaction.restore()
            self.assertEqual((release / EXE).read_bytes(), b"old")
            self.assertEqual(canary.read_bytes(), b"private fixture")
            self.assertFalse((release / "env/tool.exe").exists())
            self.assertEqual(locked.read_bytes(), b"unchanged")


if __name__ == "__main__":
    unittest.main()
