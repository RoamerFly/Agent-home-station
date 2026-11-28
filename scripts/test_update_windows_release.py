"""Synthetic checks for program replacement and private data preservation."""

import json
import io
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from update_windows_release import EXE, MIGRATION, SERVER, BotConsole, Replacement, describe_port_blockers, payload, probe_health, restore_bot_state, stop_release_for_update, update


class UpdateTests(unittest.TestCase):
    def test_bot_state_uses_local_session_and_csrf_without_persisting_credentials(self):
        release = Path(tempfile.gettempdir()) / "synthetic-release"

        class Response(io.BytesIO):
            status = 200

        class Opener:
            def __init__(self):
                self.calls = []
                self.started = False

            def open(self, request, timeout):
                self.calls.append(request)
                path = request.full_url.rsplit("/", 1)[-1]
                if path == "session":
                    return Response(b'{"authenticated":true,"csrf":"synthetic-csrf"}')
                if path == "diagnostics":
                    return Response(json.dumps({"log_directory": str(release / "data/main_setting/logs")}).encode())
                if path == "config":
                    return Response(b'{"enabled":true}')
                if path == "status":
                    return Response(b'{"bot":{"state":"running"}}' if self.started else b'{"bot":{"state":"stopped"}}')
                if path == "start":
                    self.started = True
                    return Response(b'{"ok":true}')
                raise AssertionError(path)

        opener = Opener()
        with patch("update_windows_release.urllib.request.build_opener", return_value=opener):
            self.assertEqual(BotConsole(release).state(), {"enabled": True, "running": False})
            self.assertTrue(restore_bot_state(release, {"enabled": True, "running": True}, 1))
        start = [call for call in opener.calls if call.full_url.endswith("/api/bot/start")]
        self.assertEqual(len(start), 1)
        self.assertEqual(start[0].get_header("X-csrf-token"), "synthetic-csrf")
        self.assertEqual(start[0].get_header("Origin"), "http://127.0.0.1:8080")
        self.assertEqual(start[0].data, b"{}")

    def test_disabled_bot_is_not_started_after_update(self):
        with patch("update_windows_release.BotConsole") as console:
            self.assertFalse(restore_bot_state(Path("synthetic-release"), {"enabled": False, "running": False}, 1))
        console.assert_not_called()

    def test_rollback_restores_previously_enabled_bot(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            release, candidate, work = root / "release", root / "candidate", root / "work"
            release.mkdir()
            candidate.mkdir()
            (release / EXE).write_bytes(b"old")
            (candidate / EXE).write_bytes(b"new")

            def stop_with_snapshot(_release, _timeout, on_running):
                on_running()

            with (
                patch("update_windows_release.payload", return_value={Path(EXE): candidate / EXE}),
                patch("update_windows_release.check", side_effect=[{"applications": [], "public_tools": []}, ValueError("injected")]),
                patch("update_windows_release.validate", return_value={"ok": True}),
                patch("update_windows_release.stop_release_for_update", side_effect=stop_with_snapshot),
                patch("update_windows_release.read_bot_state", return_value={"enabled": True, "running": True}),
                patch("update_windows_release.listeners", return_value=False),
                patch("update_windows_release.subprocess.Popen"),
                patch("update_windows_release.probe_health", return_value=True),
                patch("update_windows_release.restore_bot_state", return_value=True) as restore,
            ):
                with self.assertRaisesRegex(ValueError, "injected"):
                    update(SimpleNamespace(candidate=candidate, apply=True, keep_stopped=False,
                                           timeout=1, native_account="desktop"), release, work)
            self.assertEqual((release / EXE).read_bytes(), b"old")
            self.assertEqual(json.loads((work / "bot-before.json").read_text(encoding="utf-8")),
                             {"enabled": True, "running": True})
            restore.assert_called_once_with(release, {"enabled": True, "running": True}, 1)

    def test_successful_update_restores_previously_enabled_bot(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            release, candidate, work = root / "release", root / "candidate", root / "work"
            release.mkdir()
            candidate.mkdir()
            (release / EXE).write_bytes(b"old")
            (candidate / EXE).write_bytes(b"new")
            (release / "manifest.json").write_text("{}", encoding="utf-8")

            def stop_with_snapshot(_release, _timeout, on_running):
                on_running()

            with (
                patch("update_windows_release.payload", return_value={Path(EXE): candidate / EXE}),
                patch("update_windows_release.check", return_value={"applications": [], "public_tools": []}),
                patch("update_windows_release.validate", return_value={"ok": True}),
                patch("update_windows_release.migration_manifest", return_value={"schema_version": 1}),
                patch("update_windows_release.stop_release_for_update", side_effect=stop_with_snapshot),
                patch("update_windows_release.read_bot_state", return_value={"enabled": True, "running": True}),
                patch("update_windows_release.subprocess.Popen"),
                patch("update_windows_release.probe_health", return_value=True),
                patch("update_windows_release.restore_bot_state", return_value=True) as restore,
                patch("update_windows_release.sync_installed_wechat_tools", return_value=0),
            ):
                update(SimpleNamespace(candidate=candidate, apply=True, keep_stopped=False,
                                       timeout=1, native_account="desktop"), release, work)
            self.assertEqual((release / EXE).read_bytes(), b"new")
            restore.assert_called_once_with(release, {"enabled": True, "running": True}, 1)

    def test_running_release_exits_before_replacement(self):
        with tempfile.TemporaryDirectory() as directory:
            release = Path(directory)
            with (
                patch("update_windows_release.running_release_pids", side_effect=[[123], []]),
                patch("update_windows_release.listeners", return_value=[]),
                patch("update_windows_release.subprocess.run") as signal,
            ):
                self.assertTrue(stop_release_for_update(release, 1))
            signal.assert_called_once()
            self.assertEqual(signal.call_args.args[0], [str(release / EXE), "--exit-services"])

    def test_foreign_listener_cannot_trigger_release_stop(self):
        with tempfile.TemporaryDirectory() as directory:
            with (
                patch("update_windows_release.running_release_pids", return_value=[]),
                patch("update_windows_release.listeners", return_value=[8082]),
                patch("update_windows_release.port_owner_pids", return_value={8080: set(), 8082: {712}, 6174: set()}),
                patch("update_windows_release.process_image_path", return_value=r"C:\foreign\agent-home-station-server.exe"),
                patch("update_windows_release.subprocess.run") as signal,
            ):
                with self.assertRaisesRegex(RuntimeError, "unverified or foreign"):
                    stop_release_for_update(Path(directory), 1)
            signal.assert_not_called()

    def test_same_release_orphan_is_identified_but_never_started_or_killed(self):
        with tempfile.TemporaryDirectory() as directory:
            release = Path(directory)
            image = str(release / SERVER)
            with (
                patch("update_windows_release.running_release_pids", return_value=[]),
                patch("update_windows_release.listeners", return_value=[8082]),
                patch("update_windows_release.port_owner_pids", return_value={8080: set(), 8082: {901}, 6174: set()}),
                patch("update_windows_release.process_image_path", return_value=image),
                patch("update_windows_release.subprocess.run") as run,
                patch("update_windows_release.subprocess.Popen") as launch,
            ):
                with self.assertRaisesRegex(RuntimeError, r"8082: PID 901, this release"):
                    stop_release_for_update(release, 1)
            run.assert_not_called()
            launch.assert_not_called()

    def test_unverified_listener_owner_blocks_without_service_action(self):
        with tempfile.TemporaryDirectory() as directory:
            release = Path(directory)
            with (
                patch("update_windows_release.port_owner_pids", side_effect=OSError("inspection unavailable")),
                patch("update_windows_release.subprocess.run") as run,
            ):
                reason = describe_port_blockers(release, [8080, 6174])
            self.assertIn("Cannot verify listener ownership", reason)
            run.assert_not_called()

    def test_healthy_station_does_not_require_wechat_account_online(self):
        with tempfile.TemporaryDirectory() as directory:
            release = Path(directory)
            token = release / "data/applications/wechat/native/api-token"
            token.parent.mkdir(parents=True)
            token.write_text("synthetic-token", encoding="utf-8")

            class Response(io.BytesIO):
                status = 200

            class Opener:
                def open(self, request, timeout):
                    url = request if isinstance(request, str) else request.full_url
                    if url.endswith("/v1/status"):
                        return Response(b'{"state":"ready"}')
                    if "/v1/accounts/" in url:
                        return Response(b'{"authenticated":false,"connection":"disconnected"}')
                    return Response(b"ok")

            with patch("update_windows_release.urllib.request.build_opener", return_value=Opener()):
                self.assertFalse(probe_health(release, SimpleNamespace(poll=lambda: None), 1, "desktop"))

    def test_failed_replacement_restores_and_restarts_previous_release(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            release, candidate, work = root / "release", root / "candidate", root / "work"
            release.mkdir()
            candidate.mkdir()
            (release / EXE).write_bytes(b"old")
            (candidate / EXE).write_bytes(b"new")
            paths = {Path(EXE): candidate / EXE}
            with (
                patch("update_windows_release.payload", return_value=paths),
                patch("update_windows_release.check", side_effect=[{"applications": [], "public_tools": []}, ValueError("injected")]),
                patch("update_windows_release.validate", return_value={"ok": True}),
                patch("update_windows_release.listeners", return_value=False),
                patch("update_windows_release.subprocess.Popen") as launch,
                patch("update_windows_release.probe_health", return_value=False),
            ):
                with self.assertRaisesRegex(ValueError, "injected"):
                    update(SimpleNamespace(candidate=candidate, apply=True, keep_stopped=False,
                                           timeout=1, native_account="desktop"), release, work)
            self.assertEqual((release / EXE).read_bytes(), b"old")
            launch.assert_called_once()
            self.assertEqual(json.loads((work / "result.json").read_text(encoding="utf-8")),
                             {"updated": False, "rolled_back": True, "restarted": True,
                              "healthy": True, "native_login": False})

    def test_apply_while_stopped_does_not_start_account_services(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            release, candidate, work = root / "release", root / "candidate", root / "work"
            release.mkdir()
            candidate.mkdir()
            (release / EXE).write_bytes(b"old")
            (candidate / EXE).write_bytes(b"new")
            (release / "manifest.json").write_text("{}", encoding="utf-8")
            paths = {Path(EXE): candidate / EXE}
            with (
                patch("update_windows_release.payload", return_value=paths),
                patch("update_windows_release.check", return_value={"applications": [], "public_tools": []}),
                patch("update_windows_release.validate", return_value={"ok": True}),
                patch("update_windows_release.migration_manifest", return_value={"schema_version": 1}),
                patch("update_windows_release.listeners", return_value=False),
                patch("update_windows_release.subprocess.Popen") as launch,
            ):
                update(SimpleNamespace(candidate=candidate, apply=True, keep_stopped=True), release, work)
            launch.assert_not_called()
            self.assertEqual((release / EXE).read_bytes(), b"new")
            self.assertTrue((release / MIGRATION).is_file())
            result = json.loads((work / "result.json").read_text(encoding="utf-8"))
            self.assertEqual(result, {"updated": True, "kept_stopped": True, "healthy": None, "native_login": None})

    def test_private_package_sync_failure_keeps_healthy_new_program(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            release, candidate, work = root / "release", root / "candidate", root / "work"
            release.mkdir()
            candidate.mkdir()
            (release / EXE).write_bytes(b"old")
            (candidate / EXE).write_bytes(b"new")
            (release / "manifest.json").write_text("{}", encoding="utf-8")
            with (
                patch("update_windows_release.payload", return_value={Path(EXE): candidate / EXE}),
                patch("update_windows_release.check", return_value={"applications": [], "public_tools": []}),
                patch("update_windows_release.validate", return_value={"ok": True}),
                patch("update_windows_release.migration_manifest", return_value={"schema_version": 1}),
                patch("update_windows_release.stop_release_for_update"),
                patch("update_windows_release.subprocess.Popen"),
                patch("update_windows_release.probe_health", return_value=True),
                patch("update_windows_release.sync_installed_wechat_tools", side_effect=RuntimeError("synthetic sync failure")),
            ):
                with self.assertRaisesRegex(RuntimeError, "synthetic sync failure"):
                    update(SimpleNamespace(candidate=candidate, apply=True, keep_stopped=False,
                                           timeout=1, native_account="desktop"), release, work)
            self.assertEqual((release / EXE).read_bytes(), b"new")
            self.assertEqual(json.loads((work / "result.json").read_text(encoding="utf-8")),
                             {"updated": True, "restarted": True, "healthy": True,
                              "native_login": True, "installed_wechat_tools_synced": False})

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

    def test_payload_updates_standalone_rvc_control_without_models(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            candidate, release = root / "candidate", root / "release"
            package = candidate / "publictools" / "rvc-package"
            package.mkdir(parents=True)
            (package / "plugin.json").write_text(json.dumps({"id": "rvc", "files": {"run.py": "synthetic"}}), encoding="utf-8")
            (package / "run.py").write_bytes(b"plugin")
            control = package / "runtime" / "windows-station-rvc"
            control.mkdir(parents=True)
            (control / "singing_voice.py").write_bytes(b"new control")
            (control / "runtime-manifest.json").write_bytes(b"manifest")
            (package / "models").mkdir()
            (package / "models" / "weight.pth").write_bytes(b"model")
            files = payload(candidate, release)
            self.assertIn(Path("publictools/rvc-package/runtime/windows-station-rvc/singing_voice.py"), files)
            self.assertIn(Path("publictools/rvc-package/runtime/windows-station-rvc/runtime-manifest.json"), files)
            self.assertNotIn(Path("publictools/rvc-package/models/weight.pth"), files)

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
