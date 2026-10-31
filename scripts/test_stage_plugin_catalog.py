import hashlib
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("stage_plugin_catalog.py")
spec = importlib.util.spec_from_file_location("stage_plugin_catalog", SCRIPT)
stage_plugin_catalog = importlib.util.module_from_spec(spec)
spec.loader.exec_module(stage_plugin_catalog)


def write_package(root, name="synthetic", extra=None):
    package = root / name
    runtime = package / "runtime"
    runtime.mkdir(parents=True)
    member = runtime / "main.py"
    member.write_bytes(b"print('synthetic')\n")
    files = {"runtime/main.py": hashlib.sha256(member.read_bytes()).hexdigest()}
    if extra:
        for relative, content in extra.items():
            path = package / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(content)
            files[relative] = hashlib.sha256(content).hexdigest()
    (package / "plugin.json").write_text(json.dumps({"id": name, "files": files}), encoding="utf8")
    return package


class StagePluginCatalogTests(unittest.TestCase):
    def test_stages_only_indexed_source_and_is_idempotent(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source, output = root / "plugins", root / "portable" / "plugins"
            source.mkdir()
            write_package(source)
            first = stage_plugin_catalog.stage(source, output)
            self.assertFalse(first["idempotent"])
            self.assertEqual((output / "synthetic" / "runtime" / "main.py").read_bytes(), b"print('synthetic')\n")
            second = stage_plugin_catalog.stage(source, output)
            self.assertTrue(second["idempotent"])

    def test_rejects_hash_and_unindexed_source_before_output_write(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source, output = root / "plugins", root / "out"
            source.mkdir()
            package = write_package(source)
            (package / "unexpected.txt").write_text("not indexed", encoding="utf8")
            with self.assertRaisesRegex(stage_plugin_catalog.StageError, "plugin_contains_unindexed_files"):
                stage_plugin_catalog.stage(source, output)
            self.assertFalse(output.exists())
            (package / "unexpected.txt").unlink()
            (package / "runtime" / "main.py").write_text("changed", encoding="utf8")
            with self.assertRaisesRegex(stage_plugin_catalog.StageError, "plugin_manifest_hash_mismatch"):
                stage_plugin_catalog.stage(source, output)
            self.assertFalse(output.exists())

    def test_existing_mismatch_requires_fresh_directory_without_deletion(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source, output = root / "plugins", root / "out"
            source.mkdir()
            write_package(source)
            output.mkdir()
            marker = output / "keep.txt"
            marker.write_text("do not delete", encoding="utf8")
            with self.assertRaisesRegex(stage_plugin_catalog.StageError, "output_requires_fresh_directory"):
                stage_plugin_catalog.stage(source, output)
            self.assertEqual(marker.read_text(encoding="utf8"), "do not delete")

    def test_rejects_manifest_path_traversal(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source = root / "plugins"
            source.mkdir()
            package = write_package(source)
            manifest = json.loads((package / "plugin.json").read_text(encoding="utf8"))
            manifest["files"] = {"../outside.py": "0" * 64}
            (package / "plugin.json").write_text(json.dumps(manifest), encoding="utf8")
            with self.assertRaisesRegex(stage_plugin_catalog.StageError, "invalid_plugin_manifest"):
                stage_plugin_catalog.stage(source, root / "out")

    def test_rejects_symlinked_catalog_root_when_supported(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source = root / "plugins"
            source.mkdir()
            write_package(source)
            link = root / "plugins-link"
            try:
                os.symlink(source, link, target_is_directory=True)
            except OSError:
                self.skipTest("symlink creation unavailable")
            with self.assertRaisesRegex(stage_plugin_catalog.StageError, "source_catalog_unavailable"):
                stage_plugin_catalog.stage(link, root / "out")

    def test_real_official_catalog_stages_nine_packages_without_host_go_files(self):
        source = SCRIPT.parents[1] / "plugins"
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / "plugins"
            result = stage_plugin_catalog.stage(source, output)
            self.assertEqual(len(result["packages"]), 9)
            self.assertFalse((output / "catalog.go").exists())
            self.assertFalse((output / "catalog_test.go").exists())
            for package in result["packages"]:
                source_manifest = json.loads((source / package / "plugin.json").read_text(encoding="utf8"))
                for relative, digest in source_manifest["files"].items():
                    self.assertEqual(hashlib.sha256((output / package / relative).read_bytes()).hexdigest(), digest)


if __name__ == "__main__":
    unittest.main()
