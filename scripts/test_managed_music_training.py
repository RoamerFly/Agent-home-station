"""Synthetic training mount admission; never import/train models or use GPUs."""
import contextlib
import hashlib
import json
import os
from pathlib import Path
import shutil
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

import managed_music_runner as runner
import register_windows_music as registration
import rvc_train_trial as forwarder

SOURCE = Path(__file__).resolve().parents[1] / "plugins/music"
LOCAL_PYTHON = Path(__file__).resolve().parents[1] / "dist/windows-native/runtimes/windows-python-cli/python.exe"


@contextlib.contextmanager
def training_fixture(actual_python=False):
    with tempfile.TemporaryDirectory() as temporary:
        root = Path(temporary)
        controls = root / "deployment/runtimes/windows-music"
        controls.mkdir(parents=True)
        deployed_runner = controls / "managed_music_runner.py"
        shutil.copyfile(runner.__file__, deployed_runner)
        shutil.copyfile(forwarder.__file__, controls / "rvc_train_trial.py")
        package = root / "deployment/plugins/music"
        shutil.copytree(SOURCE, package, ignore=shutil.ignore_patterns("__pycache__", "*.pyc"))
        public = root / "workspace/public_workspace"
        public.mkdir(parents=True)
        private = public.parent / "pri_workspace" / hashlib.sha256(b"synthetic-account").hexdigest()
        private.mkdir(parents=True)
        python = public / "runtimes/test-python"
        if actual_python:
            shutil.copytree(LOCAL_PYTHON.parent, python, ignore=shutil.ignore_patterns("__pycache__", "*.pyc"))
        else:
            python.mkdir(parents=True)
            (python / "python.exe").write_bytes(b"synthetic unused interpreter")
        for name in ("ffmpeg.exe", "ffprobe.exe"):
            (python / name).write_bytes(b"synthetic unused media")
        resources = forwarder.training_resources(package)
        legacy = public / "projects/agent-tools/rvc_train_trial.py"
        legacy.parent.mkdir(parents=True)
        shutil.copyfile(forwarder.__file__, legacy)
        value = {"id": "rvc", "training_entry": str(legacy), "python": str(python / "python.exe")}
        registration.mark_plugin_training(value, public, resources)
        manifest = public / "manifests/rvc.json"
        manifest.parent.mkdir()
        manifest.write_text(json.dumps(value), encoding="utf8")
        with patch.object(runner, "__file__", str(deployed_runner)):
            yield dict(root=root, controls=controls, package=package, public=public, private=private,
                       python=python / "python.exe", media=python, resources=resources, legacy=legacy,
                       registration=value, manifest=manifest)


class TrainingMountAdmissionTests(unittest.TestCase):
    def test_only_explicit_deployed_or_bound_legacy_training_cli_gets_code_root(self):
        with training_fixture() as f:
            for entry in (f["resources"]["entry"], str(f["legacy"])):
                argv = [str(f["python"]), "-X", "utf8", "-B", entry, "--project", "original checkpoint",
                        "--resume-from", "original-task", "--device", "cuda:0", "train"]
                original = list(argv)
                self.assertEqual(runner.training_readonly(f["public"], argv), [str(f["package"])])
                self.assertEqual(argv, original)
            self.assertEqual(runner.training_readonly(f["public"], [str(f["python"]), "-c", "print('inference')"]), [])
            self.assertEqual(runner.training_readonly(f["public"], [str(f["python"]), "other_inference.py"]), [])

    def test_agent_manifest_cannot_select_another_valid_package_root(self):
        with training_fixture() as f:
            outside = f["root"] / "agent-selected-code/music"
            shutil.copytree(f["package"], outside)
            other = forwarder.training_resources(outside)
            f["registration"].update(training_package_directory=other["package_directory"],
                                     training_package_sha256=other["manifest_sha256"],
                                     training_entry=other["entry"], training_requirements=other["requirements"])
            f["manifest"].write_text(json.dumps(f["registration"]), encoding="utf8")
            with self.assertRaisesRegex(ValueError, "not_deployed_catalog"):
                runner.training_readonly(f["public"], [str(f["python"]), other["entry"]])

    def test_agent_import_shadow_and_changed_registration_fields_cannot_bypass_verification(self):
        for field in ("training_entry", "training_requirements", "training_package_sha256"):
            with self.subTest(field=field), training_fixture() as f:
                if field == "training_package_sha256":
                    f["registration"][field] = "0" * 64
                else:
                    f["registration"][field] = str(f["python"])
                f["manifest"].write_text(json.dumps(f["registration"]), encoding="utf8")
                shadow = types.SimpleNamespace(training_resources=lambda *unused: f["resources"], BINDING=forwarder.BINDING)
                with patch.dict(sys.modules, {"rvc_train_trial": shadow}), self.assertRaises(ValueError):
                    runner.training_readonly(f["public"], [str(f["python"]), f["resources"]["entry"]])

    def test_linked_package_member_is_denied_before_mounting(self):
        with training_fixture() as f:
            entry = Path(f["resources"]["entry"])
            original = entry.read_bytes()
            outside = f["root"] / "linked-code.py"
            outside.write_bytes(original)
            entry.unlink()
            try:
                entry.symlink_to(outside)
            except OSError:
                self.skipTest("native file symlinks unavailable")
            with self.assertRaisesRegex(ValueError, "link"):
                runner.training_readonly(f["public"], [str(f["python"]), str(entry)])

    def test_tampered_members_manifest_extra_file_and_legacy_sidecar_are_denied(self):
        for kind in ("entry", "requirements", "manifest", "unindexed", "legacy-digest", "legacy-code"):
            with self.subTest(kind=kind), training_fixture() as f:
                entry = f["resources"]["entry"]
                if kind in ("entry", "requirements"):
                    Path(f["resources"][kind]).write_bytes(b"synthetic tamper")
                elif kind == "manifest":
                    path = f["package"] / "plugin.json"
                    path.write_bytes(path.read_bytes() + b"\n")
                elif kind == "unindexed":
                    (f["package"] / "unindexed.py").write_bytes(b"# not admitted")
                else:
                    entry = str(f["legacy"])
                    if kind == "legacy-code":
                        f["legacy"].write_bytes(b"# arbitrary agent code")
                    else:
                        path = f["legacy"].with_name(forwarder.BINDING)
                        value = json.loads(path.read_text())
                        value["manifest_sha256"] = "0" * 64
                        path.write_text(json.dumps(value))
                with self.assertRaises((ValueError, OSError)):
                    runner.training_readonly(f["public"], [str(f["python"]), entry])

    def test_launch_and_run_keep_original_argv_timeout_and_other_mounts(self):
        class Child:
            def __init__(self): self.timeouts = []
            def wait(self, timeout=None): self.timeouts.append(timeout); return 0
            def poll(self): return 0
        with training_fixture() as f:
            child = Child()
            argv = [str(f["python"]), f["resources"]["entry"], "--project", "原任务", "--device", "cuda:0", "--epochs", "8", "train"]
            with patch.object(runner.sys, "platform", "win32"), patch.dict(os.environ, {
                    "WECHAT_WINDOWS_MUSIC_POLICY": "registry-read-private-network-v1",
                    "WECHAT_WINDOWS_MUSIC_MEDIA": str(f["media"])}), patch("windows_lpac.launch", return_value=child) as launch:
                runner.run(f["public"], f["private"], f["private"], argv, gpu=True, timeout=12345.75)
            self.assertEqual(launch.call_args.args[0], argv)
            self.assertEqual(launch.call_args.args[2], [str(f["private"])])
            self.assertEqual(launch.call_args.args[3], [str(f["public"]), str(f["media"]), str(f["package"])])
            self.assertEqual(child.timeouts, [12345.75])


@unittest.skipUnless(os.name == "nt" and os.environ.get("WECHAT_TRAINING_MOUNT_TEST") == "1", "opt-in synthetic project-local LPAC test")
class TrainingMountLPACReadTests(unittest.TestCase):
    def test_verified_training_cli_read_only_and_adjacent_private_unreadable(self):
        self.assertTrue(LOCAL_PYTHON.is_file(), "project-local Python required")
        with training_fixture(actual_python=True) as f:
            outside = f["root"] / "deployment/plugins/private-secret.txt"
            outside.write_text("synthetic neighbouring private data")
            body = ("import json,sys\nfrom pathlib import Path\n"
                    "entry=Path(__file__);readable=bool(entry.read_bytes());writable=False;denied=False\n"
                    "try: entry.with_name('must-not-write').write_text('forbidden');writable=True\nexcept OSError: pass\n"
                    "try: Path(" + repr(str(outside)) + ").read_bytes()\nexcept OSError: denied=True\n"
                    "print(json.dumps({'argv':sys.argv[1:],'code_readable':readable,'code_writable':writable,'private_denied':denied}))\n")
            entry = Path(f["resources"]["entry"])
            entry.write_bytes(body.encode("utf8"))
            manifest = f["package"] / "plugin.json"
            value = json.loads(manifest.read_text(encoding="utf8"))
            value["files"][forwarder.ENTRY] = hashlib.sha256(entry.read_bytes()).hexdigest()
            manifest.write_text(json.dumps(value), encoding="utf8")
            resources = forwarder.training_resources(f["package"])
            registration.mark_plugin_training(f["registration"], f["public"], resources)
            f["manifest"].write_text(json.dumps(f["registration"]), encoding="utf8")
            arguments = ["--project", "original checkpoint", "--resume-from", "original-task", "--device", "cuda:0", "train"]
            with patch.dict(os.environ, {"WECHAT_WINDOWS_MUSIC_POLICY": "registry-read-private-network-v1",
                                        "WECHAT_WINDOWS_MUSIC_MEDIA": str(f["media"])}):
                child = runner.launch_windows(f["public"], f["private"], f["private"], [str(f["python"]), "-X", "utf8", str(entry), *arguments], gpu=False)
                self.addCleanup(lambda: child.kill() if child.poll() is None else None)
                self.assertEqual(child.wait(timeout=15), 0, "synthetic code read failed")
            result = json.loads((child.log_directory / "stdout.log").read_text())
            self.assertEqual(result["argv"], arguments)
            self.assertTrue(result["code_readable"])
            self.assertFalse(result["code_writable"])
            self.assertTrue(result["private_denied"])


if __name__ == "__main__":
    unittest.main()
