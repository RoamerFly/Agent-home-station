"""Stage and update a Windows release, restarting its existing workstation."""

import argparse
import ctypes
from ctypes import wintypes
import json
import os
from pathlib import Path
import re
import shutil
import socket
import stat
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

from build_migration_manifest import build as migration_manifest
from check_portable_release import check_release, digest, safe_relative
from validate_migration_manifest import validate

SCRIPTS = Path(__file__).resolve().parent
EXE = "AgentHomeStation.exe"
SERVER = Path("data/main_setting/runtime/agent-home-station-server.exe")
MIGRATION = Path("data/main_setting/registry/migration-manifest.json")
PORTS = (8080, 8082, 6174)
MANUAL_DELETION = Path("E:/待删除")


def physical(path):
    path = Path(os.path.abspath(path))
    for part in (*reversed(path.parents), path):
        if part.is_relative_to(MANUAL_DELETION):
            raise ValueError("Path is in the manual deletion directory")
        try:
            attributes = getattr(part.lstat(), "st_file_attributes", 0)
        except FileNotFoundError:
            continue
        if attributes & stat.FILE_ATTRIBUTE_REPARSE_POINT or part.is_symlink():
            raise ValueError(f"Reparse point in update path: {part}")
    return path


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def check_active_paths(release, sources):
    registrations = [
        release / "data/main_setting/local-deployment.json",
        *(release / "data/main_setting/registry").glob("*.json"),
        *(release / "data/publictools/registrations").glob("*.json"),
        *(release / "data/tools").glob("*/config/*.json"),
    ]
    path_pattern = re.compile(r"e:[/\\]+待删除", re.IGNORECASE)
    for path in [*registrations, *sources]:
        if path.suffix.lower() not in {".json", ".py", ".pyw", ".ps1", ".cjs", ".js"}:
            continue
        if not physical(path).is_file():
            continue
        value = path.read_text(encoding="utf-8-sig").lower()
        value = value.replace(r"\u5f85\u5220\u9664", "待删除")
        if path_pattern.search(value):
            raise ValueError(f"Active path refers to the manual deletion directory: {path}")


def packages(root, area, descriptor):
    return {
        json.loads(physical(item).read_text(encoding="utf-8-sig"))["id"]: item.parent
        for item in (root / area).glob(f"*/{descriptor}")
    }


def payload(candidate, release):
    files = {Path(EXE): candidate / EXE, SERVER: candidate / SERVER}
    for source in candidate.glob("*LICENSE.txt"):
        files[Path(source.name)] = source
    for area in ("env", "applications", "publictools"):
        descriptor = {"applications": "app.json", "publictools": "plugin.json"}.get(area)
        if descriptor is None:
            for source in (candidate / area).rglob("*"):
                if source.is_file() and "__pycache__" not in source.parts:
                    files[source.relative_to(candidate)] = source
            continue
        installed = packages(release, area, descriptor)
        for identity, package in packages(candidate, area, descriptor).items():
            target = installed.get(identity, release / area / package.name)
            row = json.loads((package / descriptor).read_text(encoding="utf-8-sig"))
            for name in [descriptor, *row["files"]]:
                relative = safe_relative(name)
                if relative is None:
                    raise ValueError(f"Invalid package member in {identity}")
                files[(target / relative).relative_to(release)] = package / relative
            if identity == "wechat":
                for source in (package / "runtime/windows-x64").glob("*"):
                    if source.is_file():
                        files[(target / source.relative_to(package)).relative_to(release)] = source
    return files


def check(root, report):
    value = check_release(root)
    write_json(report, value)
    if not value["ok"]:
        raise ValueError(f"Release integrity check failed: {report}")
    return value


class Replacement:
    def __init__(self, release, transaction):
        self.release = release
        self.transaction = transaction
        self.changed = []

    def install(self, relative, source):
        target = physical(self.release / relative)
        source = physical(source)
        if target.is_file() and digest(target) == digest(source):
            return
        incoming = self.transaction / "incoming" / relative
        incoming.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source, incoming)
        if digest(incoming) != digest(source):
            raise ValueError(f"Staged file digest changed: {relative}")
        backup = None
        if target.exists():
            backup = self.transaction / "backup" / relative
            backup.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(target, backup)
            if digest(backup) != digest(target):
                raise ValueError(f"Backup digest changed: {relative}")
        target.parent.mkdir(parents=True, exist_ok=True)
        planned = [*self.changed, (target, backup)]
        pending = self.transaction / "rollback.pending.json"
        write_json(pending, [
            {"target": str(path), "backup": str(saved) if saved else None}
            for path, saved in planned
        ])
        os.replace(pending, self.transaction / "rollback.json")
        os.replace(incoming, target)
        self.changed.append((target, backup))

    def restore(self):
        failures = []
        for target, saved in reversed(self.changed):
            try:
                if saved is None:
                    target.unlink(missing_ok=True)
                else:
                    restored = self.transaction / "incoming" / target.relative_to(self.release)
                    shutil.copy2(saved, restored)
                    os.replace(restored, target)
            except OSError:
                failures.append(str(target.relative_to(self.release)))
        if failures:
            raise RuntimeError(f"Rollback incomplete for {', '.join(failures)}. Backups: {self.transaction}")


def listeners():
    result = []
    for port in PORTS:
        with socket.socket() as probe:
            probe.settimeout(0.2)
            if probe.connect_ex(("127.0.0.1", port)) == 0:
                result.append(port)
    return result


def running_release_pids(launcher):
    """Find only processes executing this exact release, not another station copy."""
    if os.name != "nt":
        raise RuntimeError("Windows process inspection is required")
    psapi = ctypes.WinDLL("Psapi.dll", use_last_error=True)
    kernel = ctypes.WinDLL("Kernel32.dll", use_last_error=True)
    psapi.EnumProcesses.argtypes = (ctypes.POINTER(wintypes.DWORD), wintypes.DWORD,
                                   ctypes.POINTER(wintypes.DWORD))
    psapi.EnumProcesses.restype = wintypes.BOOL
    kernel.OpenProcess.argtypes = (wintypes.DWORD, wintypes.BOOL, wintypes.DWORD)
    kernel.OpenProcess.restype = wintypes.HANDLE
    kernel.QueryFullProcessImageNameW.argtypes = (wintypes.HANDLE, wintypes.DWORD,
                                                  wintypes.LPWSTR, ctypes.POINTER(wintypes.DWORD))
    kernel.QueryFullProcessImageNameW.restype = wintypes.BOOL
    kernel.CloseHandle.argtypes = (wintypes.HANDLE,)
    kernel.CloseHandle.restype = wintypes.BOOL
    capacity = 1024
    while True:
        pids = (wintypes.DWORD * capacity)()
        used = wintypes.DWORD()
        if not psapi.EnumProcesses(pids, ctypes.sizeof(pids), ctypes.byref(used)):
            raise ctypes.WinError(ctypes.get_last_error())
        if used.value < ctypes.sizeof(pids):
            break
        capacity *= 2
    target = os.path.normcase(os.path.abspath(launcher))
    matches = []
    for pid in pids[:used.value // ctypes.sizeof(wintypes.DWORD)]:
        if not pid:
            continue
        handle = kernel.OpenProcess(0x1000, False, pid)
        if not handle:
            continue
        try:
            path = ctypes.create_unicode_buffer(32768)
            size = wintypes.DWORD(len(path))
            if kernel.QueryFullProcessImageNameW(handle, 0, path, ctypes.byref(size)) and \
                    os.path.normcase(os.path.abspath(path.value)) == target:
                matches.append(pid)
        finally:
            kernel.CloseHandle(handle)
    return matches


def stop_release_for_update(release, timeout):
    launcher = release / EXE
    running = running_release_pids(launcher)
    if len(running) > 1:
        raise RuntimeError("Multiple processes own this release; update not started")
    if not running:
        if listeners():
            raise RuntimeError("Workstation ports are in use by another process; update not started")
        return False
    subprocess.run([str(launcher), "--exit-services"], cwd=release, timeout=min(timeout, 15),
                   check=True, creationflags=subprocess.CREATE_NO_WINDOW)
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if not running_release_pids(launcher) and not listeners():
            return True
        time.sleep(0.2)
    raise RuntimeError("Workstation did not fully exit; update not started")


def probe_health(release, process, timeout, account):
    token_path = physical(release / "data/applications/wechat/native/api-token")
    token = token_path.read_text(encoding="utf-8-sig").strip()
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    last_check = "waiting for management services"
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline and process.poll() is None:
        try:
            for port in PORTS[:2]:
                with opener.open(f"http://127.0.0.1:{port}/healthz", timeout=2) as response:
                    if response.status != 200:
                        raise OSError("healthz")
            def native_read(path):
                request = urllib.request.Request(f"http://127.0.0.1:6174{path}",
                                                 headers={"Authorization": f"Bearer {token}"})
                with opener.open(request, timeout=2) as response:
                    return json.load(response)

            if native_read("/v1/status").get("state") != "ready":
                last_check = "native service is not ready"
            else:
                try:
                    login = native_read(f"/v1/accounts/{account}")
                except urllib.error.HTTPError as error:
                    if error.code == 404:
                        return False
                    raise
                return login.get("authenticated") is True and login.get("connection") == "online"
        except (OSError, ValueError):
            last_check = "a local health endpoint is unavailable"
        time.sleep(1)
    raise RuntimeError(f"Release readiness was not confirmed ({last_check})")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--release", required=True, type=Path)
    parser.add_argument("--candidate", type=Path, help="Use an existing verified LocalStation build")
    parser.add_argument("--wechat-runtime", type=Path)
    parser.add_argument("--media-directory", type=Path)
    parser.add_argument("--test", action="store_true")
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--keep-stopped", action="store_true", help="Apply and verify files without starting account services")
    parser.add_argument("--timeout", type=float, default=90)
    parser.add_argument("--native-account", default="desktop")
    args = parser.parse_args()
    if os.name != "nt" or args.timeout <= 0:
        parser.error("Windows and a positive timeout are required")
    if args.keep_stopped and not args.apply:
        parser.error("--keep-stopped requires --apply")
    if not re.fullmatch(r"[a-zA-Z0-9_-]+", args.native_account):
        parser.error("Invalid native account ID")
    release = physical(args.release.absolute())
    if not (release / EXE).is_file() or not (release / MIGRATION).is_file():
        parser.error("An existing Windows release with a migration manifest is required")
    lock_path = release.parent / f".{release.name}-update.lock"
    lock = lock_path.open("x", encoding="ascii")
    try:
        # Keep rename staging on the release volume. Use the repository's
        # temp directory for its normal dist/release layout.
        work_parent = release.parent
        workspace_temp = release.parent.parent / "temp"
        if release.parent.name == "dist" and workspace_temp.is_dir():
            work_parent = physical(workspace_temp)
            if os.stat(work_parent).st_dev != os.stat(release).st_dev:
                work_parent = release.parent
        work = Path(tempfile.mkdtemp(prefix=f".{release.name}-update-", dir=work_parent))
        lock.write(str(work))
        lock.flush()
        print(f"Update evidence: {work}", flush=True)
        update(args, release, work)
    finally:
        lock.close()
        lock_path.unlink()


def update(args, release, work):
    candidate = physical(args.candidate.absolute()) if args.candidate else work / "candidate"
    if candidate == release or candidate.is_relative_to(release) or release.is_relative_to(candidate):
        raise ValueError("Candidate must be separate from the live release")
    if not args.candidate:
        native = args.wechat_runtime or release / "applications/wechat/runtime/windows-x64"
        command = ["powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
                   "-File", str(SCRIPTS / "Build-Desktop.ps1"), "-OutputDirectory", str(candidate),
                   "-LocalStation", "-WithWeChatApp", "-WithBilibiliApp", "-WithDouyinApp",
                   "-WithWeChatRuntime", "-WeChatRuntimeDirectory", str(physical(native.absolute())),
                   "-WithDocumentWorkshop", "-WithAudioWorkshop", "-WithRVCBackend", "-WithComfyUI",
                   "-WithYuE", "-WithStationSpeech", "-WithAudioCpp", "-WithOBSPreview",
                   "-WithExternalTools", "-WithProjectDevelopment"]
        if args.media_directory:
            command += ["-Portable", "-MediaDirectory", str(physical(args.media_directory.absolute()))]
        if args.test:
            command.append("-Test")
        subprocess.run(command, check=True, stdin=subprocess.DEVNULL)
    check(candidate, work / "candidate-check.json")
    candidate_migration = validate(candidate, candidate / MIGRATION)
    write_json(work / "candidate-migration.json", candidate_migration)
    if not candidate_migration["ok"]:
        raise ValueError("Candidate migration manifest is invalid")
    files = payload(candidate, release)
    check_active_paths(release, files.values())
    write_json(work / "plan.json", {str(path): digest(physical(source)) for path, source in files.items()})
    if not args.apply:
        return
    old_report = validate(release, release / MIGRATION)
    write_json(work / "migration-before.json", old_report)
    if not old_report["ok"]:
        raise ValueError("Existing release migration manifest is invalid")
    if args.keep_stopped:
        if running_release_pids(release / EXE) or listeners():
            raise RuntimeError("Stop the workstation before a keep-stopped update")
    else:
        stop_release_for_update(release, args.timeout)
    transaction = Replacement(release, work)
    launcher = release / EXE
    startup = subprocess.STARTUPINFO()
    startup.dwFlags |= subprocess.STARTF_USESHOWWINDOW
    process = None
    try:
        for relative, source in files.items():
            transaction.install(relative, source)
        report = check(release, work / "release-check.json")
        metadata = migration_manifest(release)
        write_json(work / "migration-manifest.json", metadata)
        transaction.install(MIGRATION, work / "migration-manifest.json")
        migration = validate(release, release / MIGRATION)
        write_json(work / "migration-after.json", migration)
        if not migration["ok"]:
            raise ValueError("Updated release migration manifest is invalid")
        old_manifest = json.loads((release / "manifest.json").read_text(encoding="utf-8-sig"))
        for field in ("applications", "public_tools"):
            old_manifest[field] = report[field]
            for row in old_manifest[field]:
                row["path"] = Path(row["path"]).relative_to(release).as_posix()
        write_json(work / "manifest.json", old_manifest)
        transaction.install(Path("manifest.json"), work / "manifest.json")
        if args.keep_stopped:
            if listeners():
                raise RuntimeError("A workstation listener started during the stopped update")
            write_json(work / "result.json", {"updated": True, "kept_stopped": True, "healthy": None, "native_login": None})
            return
        process = subprocess.Popen([str(launcher)], cwd=release, startupinfo=startup)
        native_login = probe_health(release, process, args.timeout, args.native_account)
        write_json(work / "result.json", {"updated": True, "restarted": True,
                                          "healthy": True, "native_login": native_login})
    except (Exception, KeyboardInterrupt):
        if process and process.poll() is None:
            subprocess.run([str(launcher), "--exit-services"], cwd=release, timeout=15,
                           check=True, creationflags=subprocess.CREATE_NO_WINDOW)
            process.wait(timeout=args.timeout)
        if listeners():
            raise RuntimeError(f"Services still own the release. Rollback files retained at {work}")
        transaction.restore()
        if args.keep_stopped:
            write_json(work / "result.json", {"updated": False, "rolled_back": True,
                                              "kept_stopped": True, "healthy": None, "native_login": None})
            raise
        # A failed update must not strand the old workstation in a stopped
        # state. The files are fully restored before we launch it again.
        recovered = None
        try:
            recovered = subprocess.Popen([str(launcher)], cwd=release, startupinfo=startup)
            native_login = probe_health(release, recovered, args.timeout, args.native_account)
            write_json(work / "result.json", {"updated": False, "rolled_back": True,
                                              "restarted": True, "healthy": True, "native_login": native_login})
        except Exception:
            write_json(work / "result.json", {"updated": False, "rolled_back": True,
                                              "restart_attempted": True,
                                              "process_running": recovered is not None and recovered.poll() is None,
                                              "healthy": False, "native_login": None})
            raise RuntimeError(f"Previous release restored but its readiness was not confirmed. Evidence: {work}")
        raise


if __name__ == "__main__":
    sys.exit(main())
