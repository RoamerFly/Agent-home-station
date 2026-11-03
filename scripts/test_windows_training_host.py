"""Actual deployed Windows host -> LPAC synthetic training-code admission."""
import hashlib
import json
import os
from pathlib import Path
import runpy
import shutil
import subprocess
import time
import unittest
from unittest.mock import patch

from test_managed_music_training import training_fixture, LOCAL_PYTHON
import rvc_train_trial as forwarder
import register_windows_music as registration

SOURCE_HOST = Path(__file__).with_name("windows_agent_host.pyw")


def deployed_host(f):
    agent = f["controls"].parent / "windows-agent-host"
    agent.mkdir()
    for name in ("windows_agent_host.pyw", "windows_lpac.py"):
        shutil.copyfile(Path(__file__).with_name(name), agent / name)
    (f["controls"] / "managed_music_service.py").write_bytes(b"# synthetic unexecuted supervisor\n")
    files = {path.name: hashlib.sha256(path.read_bytes()).hexdigest() for path in f["controls"].iterdir() if path.is_file()}
    (f["controls"] / "runtime-manifest.json").write_text(json.dumps({"schema_version": 1, "platform": "windows-x64",
        "policy": "registry-read-private-network-v1", "files": files}), encoding="utf8")
    profile = f["root"] / "admin/windows-music.json"
    profile.parent.mkdir()
    profile.write_text(json.dumps({"enabled": True, "policy": "registry-read-private-network-v1", "python": str(f["python"]),
        "entry": str(f["controls"] / "managed_music_service.py"), "media": str(f["media"])}), encoding="utf8")
    return agent / "windows_agent_host.pyw", profile


def request_for(f, profile, argv):
    return {"sandbox": True, "cwd": str(f["private"]), "roots": [str(f["private"])], "read_only": [str(f["public"])],
            "argv": argv, "command": "", "shell": str(Path(os.environ.get("SystemRoot", "C:/Windows")) / "System32/cmd.exe"),
            "music_registration": str(profile), "gpu_authorized": True, "timeout_seconds": 3.625}


class WindowsTrainingHostPolicyTests(unittest.TestCase):

    def test_registered_runtime_child_is_enough_without_public_parent_mount(self):
        with training_fixture() as f:
            host, profile = deployed_host(f)
            projects = f["public"] / "projects"
            projects.mkdir(exist_ok=True)
            argv = [str(f["python"]), f["resources"]["entry"], "--help"]
            request = request_for(f, profile, argv)
            request["roots"] = [str(f["private"]), str(projects)]
            request["read_only"] = [str(f["public"] / "runtimes")]
            namespace = runpy.run_path(str(host))
            self.assertEqual(namespace["training_mount"](request, argv, ""), (argv, [str(f["package"])]))
            # A visible project with a runtime-looking child cannot prove the
            # trusted interpreter's mount.  The public parent itself is never
            # implicitly added by training admission.
            fake = projects / "runtimes"
            fake.mkdir()
            request["read_only"] = [str(fake)]
            with self.assertRaisesRegex(ValueError, "public_workspace_required"):
                namespace["training_mount"](request, argv, "")
            # Nor can a profile point at editable project code masquerading as
            # its interpreter, even if that binary-looking file exists.
            imposter = projects / "python.exe"
            imposter.write_bytes(b"synthetic")
            value = json.loads(profile.read_text(encoding="utf8"))
            value["python"] = str(imposter)
            profile.write_text(json.dumps(value), encoding="utf8")
            request["read_only"] = [str(f["public"] / "runtimes")]
            with self.assertRaisesRegex(ValueError, "registered_interpreter_required"):
                namespace["training_mount"](request, argv, "")

    def test_cpu_missing_registration_and_changed_control_profile_are_rejected(self):
        for change in ("cpu", "registration", "control", "package", "entry", "interpreter"):
            with self.subTest(change=change), training_fixture() as f:
                host, profile = deployed_host(f)
                request = request_for(f, profile, [str(f["python"]), f["resources"]["entry"], "--help"])
                if change == "cpu": request["gpu_authorized"] = False
                elif change == "registration": request["music_registration"] = ""
                elif change == "control": (f["controls"] / "managed_music_runner.py").write_bytes(b"# tampered helper")
                elif change == "package": Path(f["resources"]["entry"]).write_bytes(b"# tampered training code")
                else:
                    value = json.loads(profile.read_text(encoding="utf8"))
                    value["entry" if change == "entry" else "python"] = str(f["python"] if change == "entry" else host)
                    profile.write_text(json.dumps(value), encoding="utf8")
                namespace = runpy.run_path(str(host))
                with self.assertRaises((ValueError, OSError)):
                    namespace["training_mount"](request, request["argv"], "")

    def test_only_code_mount_is_added_without_changing_profile_capabilities_or_argv(self):
        class Child:
            pid = 1
            returncode = 0
            def poll(self): return 0
        with training_fixture() as f:
            host, profile = deployed_host(f)
            argv = [str(f["python"]), "-X", "utf8", f["resources"]["entry"], "--project", "original checkpoint", "--device", "cuda:0", "train"]
            request = request_for(f, profile, argv)
            request["training_read_only"] = [str(f["root"] / "must-not-mount")]
            spool = f["root"] / "control"
            spool.mkdir()
            path = spool / "request.json"
            path.write_text(json.dumps(request), encoding="utf8")
            with patch("windows_lpac.launch", return_value=Child()) as launch:
                runpy.run_path(str(host))["run"](path)
            result = json.loads((spool / "result.json").read_text(encoding="utf8"))
            self.assertEqual(result["exit_code"], 0, result)
            self.assertEqual(launch.call_args.args[0], argv)
            self.assertEqual(launch.call_args.args[2], request["roots"])
            self.assertEqual(launch.call_args.args[3], [*request["read_only"], str(f["package"])])
            self.assertFalse(launch.call_args.kwargs["music_runtime"])
            self.assertIsNone(launch.call_args.kwargs["environment"])
            self.assertEqual(json.loads(path.read_text(encoding="utf8"))["timeout_seconds"], 3.625)

    def test_ordinary_python_does_not_even_require_a_training_deployment(self):
        namespace = runpy.run_path(str(SOURCE_HOST))
        argv = ["C:/registered/python.exe", "-c", "print('ordinary program')"]
        self.assertEqual(namespace["training_mount"]({}, argv, ""), (argv, []))


@unittest.skipUnless(os.name == "nt" and os.environ.get("WECHAT_TRAINING_HOST_TEST") == "1", "opt-in project-local native LPAC host test")
class WindowsTrainingHostLPAC(unittest.TestCase):
    def execute(self, f, host, request):
        spool = f["root"] / ("control-" + str(time.monotonic_ns()))
        spool.mkdir()
        path = spool / "request.json"
        path.write_text(json.dumps(request), encoding="utf8")
        child = subprocess.Popen([str(LOCAL_PYTHON.with_name("pythonw.exe")), "-B", str(host), str(path)],
                                 stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                 creationflags=subprocess.CREATE_NO_WINDOW)
        try:
            self.assertEqual(child.wait(timeout=15), 0)
            return spool, json.loads((spool / "result.json").read_text(encoding="utf8"))
        finally:
            if child.poll() is None: child.kill(); child.wait()

    def install_synthetic_cli(self, f, body):
        entry = Path(f["resources"]["entry"])
        entry.write_bytes(body.encode("utf8"))
        manifest = f["package"] / "plugin.json"
        value = json.loads(manifest.read_text(encoding="utf8"))
        value["files"][forwarder.ENTRY] = hashlib.sha256(entry.read_bytes()).hexdigest()
        manifest.write_text(json.dumps(value), encoding="utf8")
        resources = forwarder.training_resources(f["package"])
        registration.mark_plugin_training(f["registration"], f["public"], resources)
        f["manifest"].write_text(json.dumps(f["registration"]), encoding="utf8")
        return str(entry)

    def test_actual_host_direct_and_single_native_command_read_verified_cli_only(self):
        self.assertTrue(LOCAL_PYTHON.is_file())
        with training_fixture(actual_python=True) as f:
            host, profile = deployed_host(f)
            outside = f["root"] / "deployment/plugins/private.txt"
            outside.write_bytes(b"synthetic adjacent private data")
            body = ("import json,sys\nfrom pathlib import Path\n"
                    "readable=bool(Path(__file__).read_bytes());denied=False;writable=False\n"
                    "try: Path(" + repr(str(outside)) + ").read_bytes()\nexcept OSError: denied=True\n"
                    "try: Path(__file__).with_name('must-not-write').write_text('bad');writable=True\nexcept OSError: pass\n"
                    "print(json.dumps({'argv':sys.argv[1:],'readable':readable,'private_denied':denied,'writable':writable}))\n")
            entry = self.install_synthetic_cli(f, body)
            arguments = ["--project", "原 checkpoint", "--resume-from", "original-task", "--device", "cuda:0", "", 'quoted"value', "trailing path\\", "train"]
            argv = [str(f["python"]), "-X", "utf8", entry, *arguments]
            for direct in (True, False):
                request = request_for(f, profile, argv)
                if not direct: request.update(argv=None, command=subprocess.list2cmdline(argv))
                spool, result = self.execute(f, host, request)
                self.assertEqual(result["exit_code"], 0, result)
                observed = json.loads((spool / "output.log").read_text(encoding="utf8"))
                self.assertEqual(observed["argv"], arguments)
                self.assertTrue(observed["readable"])
                self.assertTrue(observed["private_denied"])
                self.assertFalse(observed["writable"])

    def test_fractional_timeout_and_tampered_package_cannot_launch(self):
        with training_fixture(actual_python=True) as f:
            host, profile = deployed_host(f)
            entry = self.install_synthetic_cli(f, "import time\ntime.sleep(5)\n")
            request = request_for(f, profile, [str(f["python"]), entry])
            request["timeout_seconds"] = .375
            started = time.monotonic()
            _, result = self.execute(f, host, request)
            self.assertTrue(result.get("timed_out"), result)
            self.assertLess(time.monotonic() - started, 3)
            self.assertNotEqual(result["exit_code"], 0)
            Path(entry).write_text("raise SystemExit('tampered code executed')", encoding="utf8")
            request["timeout_seconds"] = 3.625
            spool, result = self.execute(f, host, request)
            self.assertIn("member_changed", result.get("error", ""))
            self.assertFalse((spool / "running.json").exists())


if __name__ == "__main__": unittest.main()
