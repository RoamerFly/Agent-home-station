"""Register already verified project-local Windows music resources.

This does not start models, change persona/default voice identities, run WSL,
resume old jobs or send media. Existing registrations/configs are backed up.
"""
import argparse
import hashlib
from importlib import metadata
import json
import os
from pathlib import Path
import shutil
import uuid
import subprocess

from prepare_windows_python import prepare, digest
from install_windows_gpt_nltk import install as install_gpt_nltk
from register_gpt_sovits import install_plugin_entry, mark_plugin_inference
from rvc_train_trial import BINDING as TRAINING_BINDING, training_resources

POLICY = "registry-read-private-network-v1"


def deployed_training_resources(program_runtimes):
    """Read only the shipped catalog; never search a repository source tree."""
    package = Path(program_runtimes).parent / "plugins/music"
    if not package.exists():
        return None
    return training_resources(package)


def mark_plugin_training(value, public, resources):
    """Update tool declarations without moving voices, jobs or checkpoints."""
    legacy_entries = [value.get(name) for name in ("training_entry", "training_script", "training_cli")]
    legacy_entries.append(str(Path(public) / "projects/agent-tools/rvc_train_trial.py"))
    for candidate in dict.fromkeys(p for p in legacy_entries if isinstance(p, str) and p):
        path = Path(candidate)
        if path.name != "rvc_train_trial.py" or not path.exists():
            continue
        # A catalog entry is already the implementation, never replace it.
        if resources and path == Path(resources["entry"]):
            continue
        if any(part.is_symlink() or getattr(part, "is_junction", lambda: False)() for part in (path, *path.parents)) or not path.is_file() or not path.resolve().is_relative_to((Path(public) / "projects").resolve()):
            raise ValueError("training_cli_outside_registered_projects")
        if resources:
            publish_json(path.with_name(TRAINING_BINDING), {"version": 1,
                         "package_directory": resources["package_directory"],
                         "manifest_sha256": resources["manifest_sha256"]}, backup=False)
        stub = Path(__file__).with_name("rvc_train_trial.py").read_text(encoding="utf8")
        if path.read_text(encoding="utf8") != stub:
            pending = path.with_suffix(".pending-" + uuid.uuid4().hex)
            pending.write_text(stub, encoding="utf8", newline="\n")
            pending.replace(path)
    value["training_owner"] = "plugin:music"
    value["training_product_api"] = "pending"
    value["training_status"] = "registered_explicit_project_cli" if resources else "requires_deployed_music_training_package"
    value["training_instruction"] = (
        "Run training_entry only through the explicitly authorized registered project execution; "
        "GPU use still requires gpu=true and the registered CUDA interpreter/profile. "
        "Preserve the selected voice, --project, --dataset, --experiment, --resume-from, --device and all original parameters. "
        "Install training_requirements into a separate dependency directory; pass each training_python_paths directory with --extra-python-path. "
        "prepare/train/index, optimizer and final export checks are owned by the installed music package. music.train has no product API.")
    if resources:
        value.update(training_entry=resources["entry"], training_requirements=resources["requirements"],
                     training_package_directory=resources["package_directory"],
                     training_package_sha256=resources["manifest_sha256"], training_package_version=resources["version"])
    else:
        value["training_instruction"] = "Install/deploy and verify the music package training resources before invoking the historical training CLI. " + value["training_instruction"]


def restore_mount_inheritance(root):
    """Public resources/account mounts retain their parent's scoped ACL.

    The migration copier creates owner-only child directories. Those protected
    children stop a later LPAC root grant from inheriting. Enable inheritance
    only inside the explicitly registered tree; never change owner/root ACL.
    """
    for base, dirs, files in os.walk(root, followlinks=False):
        for name in [*dirs, *files]:
            path = Path(base, name)
            if path.is_symlink() or path.is_junction():
                raise ValueError("music_resource_link_requires_review")
    native = str(root)
    if not native.startswith("\\\\?\\"):
        native = "\\\\?\\" + native
    subprocess.run([str(Path(os.environ["SystemRoot"]) / "System32/icacls.exe"), native,
                    "/inheritance:e", "/T", "/L", "/Q"], check=True,
                   stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
                   creationflags=subprocess.CREATE_NO_WINDOW)


def publish_json(path, value, backup=True):
    path.parent.mkdir(parents=True, exist_ok=True)
    text = json.dumps(value, ensure_ascii=False, indent=2)
    if path.exists():
        if path.read_text(encoding="utf8") == text:
            return
        if backup:
            backups = path.parent / ".backups"
            backups.mkdir(exist_ok=True)
            shutil.copy2(path, backups / (path.name + "." + uuid.uuid4().hex + ".bak"))
    pending = path.with_suffix(".pending-" + uuid.uuid4().hex)
    pending.write_text(text, encoding="utf8")
    pending.replace(path)


def register(workspace, legacy_public, admin, program_runtimes, authorize=False, repair_inheritance=True):
    if os.name != "nt" or not authorize:
        raise ValueError("explicit_windows_music_policy_authorization_required")
    workspace, legacy_public, admin, program_runtimes = [Path(p).resolve(strict=True) for p in
                                                        (workspace, legacy_public, admin, program_runtimes)]
    training = deployed_training_resources(program_runtimes)
    public = workspace / "public_workspace"
    packages = public / "runtimes/windows-music-packages"
    if not (packages / "torch").is_dir() or not (packages / "aiohttp").is_dir():
        raise ValueError("verified_windows_music_packages_required")
    interpreter = public / "runtimes/windows-music-python"
    bundle = program_runtimes / "windows-python-cli"
    if not interpreter.exists() and bundle.is_dir():
        # Reuse the already shipped and verified CLI; an installed desktop
        # must not download a second identical interpreter to enable music.
        record = prepare(bundle, public / "runtimes/downloads")
        staging = interpreter.with_name(interpreter.name + ".staging-" + uuid.uuid4().hex)
        shutil.copytree(bundle, staging, ignore=shutil.ignore_patterns("__pycache__", "*.pyc"))
        if any(digest(staging / name) != checksum for name, checksum in record["files"].items()):
            raise ValueError("copied_music_python_integrity_failed")
        staging.rename(interpreter)
    prepare(interpreter, public / "runtimes/downloads")
    pth = interpreter / "python313._pth"
    pth.write_text("python313.zip\n.\n../windows-music-packages\n../windows-music-extras\n../../projects/agent-tools\nimport site\n", encoding="utf8")
    shutil.copy2(Path(__file__).with_name("windows_music_site.py"), interpreter / "windows_music_site.py")
    (interpreter / "sitecustomize.py").write_text("from windows_music_site import initialize\ninitialize()\n", encoding="utf8")
    manifest = json.loads((interpreter / "runtime-manifest.json").read_text(encoding="utf8"))
    manifest["files"][pth.name] = digest(pth)
    for name in ("windows_music_site.py", "sitecustomize.py"):
        manifest["files"][name] = digest(interpreter / name)
    manifest["purpose"] = "owner-authorized isolated music/GPU runtime"
    publish_json(interpreter / "runtime-manifest.json", manifest)
    installed = {}
    for directory in (packages, public / "runtimes/windows-music-extras"):
        if directory.exists():
            for distribution in metadata.distributions(path=[str(directory)]):
                installed[distribution.metadata["Name"]] = distribution.version
    publish_json(public / "runtimes/windows-music-package-versions.json", {"platform": "windows-x64", "packages": installed})
    overlay = public / "runtimes/windows-gpt-packages"
    if overlay.is_dir():
        install_gpt_nltk(public / "runtimes/windows-gpt-nltk-data", public / "runtimes/downloads/nltk")
        versions = {d.metadata["Name"]: d.version for d in metadata.distributions(path=[str(overlay)])}
        publish_json(public / "runtimes/windows-gpt-package-versions.json", {"platform": "windows-x64", "packages": versions})
    training_overlay = public / "runtimes/windows-rvc-training-packages"
    if training_overlay.is_dir():
        versions = {d.metadata["Name"]: d.version for d in metadata.distributions(path=[str(training_overlay)])}
        publish_json(public / "runtimes/windows-rvc-training-package-versions.json", {"platform": "windows-x64", "packages": versions})
    old = json.loads((legacy_public / "manifests/rvc.json").read_text(encoding="utf8"))
    prefix = old["root"].rsplit("/projects/", 1)[0]
    python = str(interpreter / "python.exe")

    def paths(value):
        if isinstance(value, dict):
            return {k: python if k in ("python", "rvc_python", "full_cover_python") and isinstance(v, str) else paths(v)
                    for k, v in value.items()}
        if isinstance(value, list):
            return [paths(x) for x in value]
        if isinstance(value, str) and (value == prefix or value.startswith(prefix + "/")):
            return str(public / value[len(prefix):].lstrip("/"))
        return value

    for source in (legacy_public / "manifests").glob("*.json"):
        if source.name.startswith("asr-") or ".backup-" in source.name:
            continue
        value = paths(json.loads(source.read_text(encoding="utf8")))
        if value.get("id") == "gpt-sovits" and overlay.is_dir():
            value["packages"] = str(overlay)
        if value.get("id") == "gpt-sovits":
            project = Path(value["root"])
            if not project.resolve().is_relative_to(public.resolve()):
                raise ValueError("gpt_sovits_project_outside_registered_public_root")
            mark_plugin_inference(value, install_plugin_entry(project))
        if value.get("id") == "yue":
            value["usage"] = value.get("usage", "").replace(
                "服务在WSL本机Bubblewrap运行", "Windows 服务在受管音乐/GPU 后端运行")
        if value.get("id") == "gpt-sovits" and training_overlay.is_dir():
            value["training_packages"] = str(training_overlay)
        if value.get("id") == "rvc":
            if training_overlay.is_dir():
                value["training_python_paths"] = [str(training_overlay)]
            mark_plugin_training(value, public, training)
        if value.get("id") != "gpt-sovits":
            value["execution"] = "Windows native; owner-authorized music/GPU LPAC; account-scoped named pipe; start via music_service_start"
            value["runtime_status"] = "registered_pending_windows_model_validation"
        publish_json(public / "manifests" / source.name, value, backup=value.get("id") != "gpt-sovits")

    def voice_config(file):
        value = json.loads(file.read_text(encoding="utf8"))
        if value.get("engine") != "rvc":
            return None
        value = paths(value)
        value["python"] = value["rvc_python"] = python
        value["rvc_project"] = str(public / "projects/rvc-upstream")
        value["runtime"] = str(public / "models/separator")
        value["rvc_device"] = "cuda:0"
        for name in ("rvc_model", "rvc_index"):
            path = Path(value[name])
            if not path.is_file():
                path = file.parent / path.name
            if not path.is_file() or digest(path) != value.get(name + "_sha256"):
                raise ValueError("registered_voice_weight_missing_or_changed")
            value[name] = str(path)
        return value

    default = voice_config(legacy_public / "manifests/singing-voice.config")
    publish_json(public / "manifests/singing-voice.config", default)
    count = 0
    roots = [public / "models/voice-library"]
    roots += [p / "voice-library" for p in (workspace / "pri_workspace").iterdir() if p.is_dir() and not p.is_symlink()]
    for library in roots:
        if not library.exists():
            continue
        manifests = library / "manifests" if library.parent.name != "models" else public / "manifests"
        for registration in manifests.glob("*.json"):
            if ".backup-" in registration.name:
                continue
            value = json.loads(registration.read_text(encoding="utf8"))
            if not isinstance(value.get("configuration"), str):
                continue
            config = Path(paths(value["configuration"]))
            if not config.is_file() or not config.resolve().is_relative_to(library.resolve()):
                continue
            # Preserve old bytes and job fingerprints; new platform gets an
            # explicit per-voice config, retaining its exact model/index hashes.
            adapted = voice_config(config)
            if adapted is None:
                continue
            target = config.with_name(config.stem + ".windows.json") if not config.stem.endswith(".windows") else config
            publish_json(target, adapted)
            value["configuration"] = str(target)
            publish_json(registration, value)
            count += 1
    if repair_inheritance:
        restore_mount_inheritance(public)
    # Each account keeps its own root policy; sibling account roots are never
    # granted a SID by this operation. No control-plane directory is included.
    for library in roots[1:]:
        if repair_inheritance and library.exists():
            restore_mount_inheritance(library.parent)
    controls = program_runtimes / "windows-music"
    entry = controls / "managed_music_service.py"
    if not entry.is_file() or not (program_runtimes / "windows-media/ffmpeg.exe").is_file():
        raise ValueError("bundled_windows_music_controls_required")
    ipc = admin / "windows-music-ipc"
    ipc.mkdir(mode=0o700, exist_ok=True)
    publish_json(admin / "windows-music.json", {"enabled": True, "policy": POLICY, "python": python,
                                               "entry": str(entry), "media": str(program_runtimes / "windows-media"), "ipc": str(ipc)})
    print(json.dumps({"registered": True, "platform": "windows-x64", "voice_configs": count,
                      "package_versions": len(installed), "policy": POLICY, "model_tasks_started": 0, "wechat_sends": 0}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--workspace", required=True)
    parser.add_argument("--legacy-public", required=True)
    parser.add_argument("--admin", required=True)
    parser.add_argument("--program-runtimes", required=True)
    parser.add_argument("--authorize-registry-and-private-network", action="store_true")
    parser.add_argument("--inheritance-already-verified", action="store_true", help="skip repeating the full initial migration ACL repair")
    args = parser.parse_args()
    register(args.workspace, args.legacy_public, args.admin, args.program_runtimes, args.authorize_registry_and_private_network,
             not args.inheritance_already_verified)
