import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import prepare_native_docker


class NativeContextTests(unittest.TestCase):
    def test_patch_applies_inside_ignored_nested_context(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            def git(directory, *args):
                return subprocess.check_output(["git", "-C", str(directory), *args])
            git(root, "init", "-q")
            source = root / "upstream"
            source.mkdir()
            git(source, "init", "-q")
            file = source / "packages/native-client-rust/synthetic.rs"
            file.parent.mkdir(parents=True)
            file.write_text("original\n")
            git(source, "add", ".")
            git(source, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", "base")
            base = git(source, "rev-parse", "HEAD").decode().strip()
            file.write_text("patched\n")
            change = git(source, "diff", "HEAD")
            (root / "reviewed.patch").write_bytes(change)
            manifest = root / "components/native-client/source-manifest.json"
            manifest.parent.mkdir(parents=True)
            manifest.write_text(json.dumps({"base_commit": base, "patch": "reviewed.patch", "patch_sha256": hashlib.sha256(change).hexdigest()}))
            (root / "temp").mkdir()
            output = root / "temp/context"
            with patch.object(prepare_native_docker, "ROOT", root):
                prepare_native_docker.prepare(source, output)
            self.assertEqual((output / "packages/native-client-rust/synthetic.rs").read_text(), "patched\n")
            self.assertNotIn("original", (output / "packages/native-client-rust/synthetic.rs").read_text())


if __name__ == "__main__":
    unittest.main()
