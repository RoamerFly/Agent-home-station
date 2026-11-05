"""Offline installed-package CLI/registration tests; no training or models."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

import register_windows_music as registration
import rvc_train_trial as forwarder


SOURCE = Path(__file__).resolve().parents[1] / "plugins/music"


class TrainingForwarderTests(unittest.TestCase):
    def package(self, root, synthetic=False):
        target = root / "deployment/plugins/music"
        shutil.copytree(SOURCE, target, ignore=shutil.ignore_patterns("__pycache__", "*.pyc"))
        manifest = target / "plugin.json"
        value = json.loads(manifest.read_text(encoding="utf8"))
        if synthetic:
            entry = target / forwarder.ENTRY
            entry.write_text("import json,sys\nprint(json.dumps(sys.argv[1:],ensure_ascii=True))\nraise SystemExit(17)\n", encoding="utf8")
            value["files"][forwarder.ENTRY] = hashlib.sha256(entry.read_bytes()).hexdigest()
            manifest.write_text(json.dumps(value), encoding="utf8")
        return forwarder.training_resources(target)

    def cli(self, root, resources=None):
        target = root / "legacy-project/rvc_train_trial.py"
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(forwarder.__file__, target)
        if resources:
            target.with_name(forwarder.BINDING).write_text(json.dumps({"version": 1,
                "package_directory": resources["package_directory"],
                "manifest_sha256": resources["manifest_sha256"]}), encoding="utf8")
        return target

    def run_cli(self, cli, arguments, environment=None):
        env = dict(os.environ)
        for name in ("WECHAT_MUSIC_PACKAGE_DIR", "WECHAT_MUSIC_PACKAGE_SHA256"):
            env.pop(name, None)
        env.update(environment or {})
        return subprocess.run([sys.executable, "-I", "-B", str(cli), *arguments], cwd=cli.parent,
                              env=env, text=True, encoding="utf8", capture_output=True)

    def test_actual_deployed_training_help_without_repository_imports(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            resources = self.package(root)
            cli = self.cli(root, resources)
            result = self.run_cli(cli, ["--help"])
            self.assertEqual(result.returncode, 0, result.stderr)
            for option in ("--project", "--source-project", "--resume-from", "--extra-python-path", "--device", "{prepare,train,index}"):
                self.assertIn(option, result.stdout)
            self.assertIn("matplotlib==3.9.4", Path(resources["requirements"]).read_text())

    def test_original_arguments_and_exit_status_are_forwarded_exactly(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            resources = self.package(root, synthetic=True)
            cli = self.cli(root, resources)
            arguments = ["--python", "registered venv/python", "--project", "原 checkpoint", "--resume-from", "original-task",
                         "--voice-id", "selected-private-voice", "--device", "cuda:0", "", 'quoted"value', "tail\\"]
            result = self.run_cli(cli, arguments)
            self.assertEqual(result.returncode, 17, result.stderr)
            self.assertEqual(json.loads(result.stdout), arguments)

    def test_explicit_environment_pair_overrides_sidecar_without_fallback(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            resources = self.package(root, synthetic=True)
            cli = self.cli(root)
            cli.with_name(forwarder.BINDING).write_text("invalid old binding", encoding="utf8")
            environment = {"WECHAT_MUSIC_PACKAGE_DIR": resources["package_directory"],
                           "WECHAT_MUSIC_PACKAGE_SHA256": resources["manifest_sha256"]}
            self.assertEqual(self.run_cli(cli, ["preserved"], environment).returncode, 17)
            cli.with_name(forwarder.BINDING).write_text(json.dumps({"version": 1,
                "package_directory": resources["package_directory"],
                "manifest_sha256": resources["manifest_sha256"]}), encoding="utf8")
            environment.pop("WECHAT_MUSIC_PACKAGE_SHA256")
            self.assertEqual(self.run_cli(cli, ["--help"], environment).returncode, 2)
            self.assertEqual(self.run_cli(cli, ["--help"], {"WECHAT_MUSIC_PACKAGE_SHA256": resources["manifest_sha256"]}).returncode, 2)

    def test_missing_binding_never_searches_a_nearby_source_package(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.package(root)
            cli = self.cli(root)
            # A source-layout directory is deliberately present beside the CLI.
            shutil.copytree(SOURCE, root / "plugins/music")
            result = self.run_cli(cli, ["--help"])
            self.assertEqual(result.returncode, 2)
            self.assertEqual(result.stdout, "")
            self.assertEqual(result.stderr.strip(), "installed_music_training_package_missing_or_changed")

    def test_changed_code_manifest_and_unindexed_files_cannot_execute(self):
        for changed in ("entry", "manifest", "unindexed"):
            with self.subTest(changed=changed), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                resources = self.package(root, synthetic=True)
                cli = self.cli(root, resources)
                package = Path(resources["package_directory"])
                if changed == "entry":
                    (package / forwarder.ENTRY).write_text("raise SystemExit(99)\n", encoding="utf8")
                elif changed == "manifest":
                    manifest = package / "plugin.json"
                    manifest.write_bytes(manifest.read_bytes() + b"\n")
                else:
                    (package / "unindexed.py").write_text("# not admitted\n", encoding="utf8")
                result = self.run_cli(cli, ["--help"])
                self.assertEqual(result.returncode, 2)
                self.assertEqual(result.stdout, "")

    def test_windows_tool_registration_preserves_voice_and_original_files(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            resources = self.package(root)
            runtimes = root / "deployment/runtimes"
            runtimes.mkdir()
            public = root / "workspace/public_workspace"
            cli = public / "projects/agent-tools/rvc_train_trial.py"
            cli.parent.mkdir(parents=True)
            cli.write_text("# historical copied training helper\n", encoding="utf8")
            model, checkpoint = public / "original-model.pth", public / "original-task.json"
            model.write_bytes(b"synthetic original selected voice")
            checkpoint.write_text('{"task":"kept"}', encoding="utf8")
            value = {"id": "rvc", "training_entry": str(cli), "selected_voice": "private-original",
                     "model": str(model), "checkpoint": str(checkpoint), "training_python_paths": ["original-dependencies"]}
            before = (model.read_bytes(), checkpoint.read_bytes())
            registration.mark_plugin_training(value, public, registration.deployed_training_resources(runtimes))
            self.assertEqual(value["training_entry"], resources["entry"])
            self.assertEqual(value["training_requirements"], resources["requirements"])
            self.assertEqual(value["training_package_sha256"], resources["manifest_sha256"])
            self.assertEqual(value["training_owner"], "plugin:music")
            self.assertEqual(value["training_product_api"], "pending")
            self.assertEqual(value["selected_voice"], "private-original")
            self.assertEqual(value["training_python_paths"], ["original-dependencies"])
            self.assertEqual((model.read_bytes(), checkpoint.read_bytes()), before)
            self.assertEqual(self.run_cli(cli, ["--help"]).returncode, 0)
            first = dict(value)
            registration.mark_plugin_training(value, public, resources)
            self.assertEqual(value, first)
            self.assertFalse(list(public.rglob(".backups")))

    def test_missing_training_package_is_explicit_and_outside_cli_is_not_written(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            public = root / "public"
            public.mkdir()
            value = {"id": "rvc", "selected_voice": "kept"}
            registration.mark_plugin_training(value, public, None)
            self.assertEqual(value["training_status"], "requires_deployed_music_training_package")
            self.assertEqual(value["selected_voice"], "kept")
            outside = root / "outside/rvc_train_trial.py"
            outside.parent.mkdir()
            outside.write_text("# keep\n", encoding="utf8")
            with self.assertRaisesRegex(ValueError, "outside_registered_projects"):
                registration.mark_plugin_training({"training_entry": str(outside)}, public, None)
            self.assertEqual(outside.read_text(encoding="utf8"), "# keep\n")


if __name__ == "__main__":
    unittest.main()
