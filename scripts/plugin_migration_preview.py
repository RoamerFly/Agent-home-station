#!/usr/bin/env python3
"""Build a read-only, per-account legacy-to-plugin migration plan.

This intentionally *does not* install packages, edit configuration databases,
enable plugins, copy models, or copy history. It is the input for a later
transactional applier: an administrator reviews the public plan and may
explicitly request a separate 0600 host-secret handoff file for an isolated
target. The normal report never contains secret values.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import sys
from typing import Any


SCHEMA_VERSION = 1
MAX_JSON = 1 << 20
SPEECH_PATH = "/api/v1/services/audio/tts/SpeechSynthesizer"
ASR_PATH = "/api/v1/services/aigc/multimodal-generation/generation"


def _sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def _account_dir(account: str) -> str:
    return hashlib.sha256(account.encode("utf-8")).hexdigest()


def _read_json(path: Path, *, required: bool = False) -> tuple[dict[str, Any], str | None]:
    """Read one small, regular JSON source without modifying it."""
    if not path.exists():
        return {}, "missing" if required else None
    if path.is_symlink() or not path.is_file():
        return {}, "not_regular"
    try:
        data = path.read_bytes()
    except OSError:
        return {}, "unreadable"
    if len(data) > MAX_JSON:
        return {}, "too_large"
    try:
        value = json.loads(data.decode("utf-8-sig"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        return {}, "invalid_json"
    if not isinstance(value, dict):
        return {}, "not_object"
    return value, None


def _safe_text(value: Any) -> str:
    return value if isinstance(value, str) else ""


def _join_endpoint(root: str, suffix: str) -> str:
    root = _safe_text(root).rstrip("/")
    return root + suffix if root else ""


def _secret(value: Any, source: str) -> tuple[dict[str, Any], str | None]:
    """Return a report-safe descriptor plus the actual secret for opt-in handoff."""
    text = _safe_text(value)
    report: dict[str, Any] = {"source_field": source, "present": bool(text)}
    if text:
        report["sha256"] = _sha256(text.encode("utf-8"))
    return report, text or None


def _package(repo: Path, plugin_id: str) -> dict[str, Any]:
    manifest = repo / "plugins" / plugin_id / "plugin.json"
    value, error = _read_json(manifest, required=True)
    out: dict[str, Any] = {
        "id": plugin_id,
        "source_manifest": str(manifest),
        "status": value.get("status") if not error else "unavailable",
        "version": value.get("version") if not error else None,
        "manifest_sha256": _sha256(manifest.read_bytes()) if not error else None,
    }
    if error:
        out["manifest_error"] = error
    return out


def _step(plugin_id: str, package: dict[str, Any], legacy_enabled: bool, values: dict[str, Any], secrets: dict[str, Any], required_missing: list[str], unmapped: list[str] | None = None, profile_id: str | None = None) -> dict[str, Any]:
    status = "ready_for_administrator_review"
    if package["status"] != "ready" or required_missing:
        status = "blocked"
    result: dict[str, Any] = {
        "plugin": plugin_id,
        "package": package,
        "expected_config_revision": 0,
        "legacy_enabled": legacy_enabled,
        "proposed_enabled": False,
        "eligible_to_enable_after_confirmation": status == "ready_for_administrator_review" and legacy_enabled,
        "status": status,
        "values": values,
        "secrets": secrets,
        "required_missing": sorted(set(required_missing)),
    }
    if unmapped:
        result["unmapped_legacy_fields"] = sorted(set(unmapped))
    if profile_id:
        result["profile_id"] = profile_id
    return result


def _speech_plan(repo: Path, speech: dict[str, Any]) -> tuple[dict[str, Any], dict[str, str]]:
    enabled = bool(speech.get("tts_enabled"))
    key_report, key = _secret(speech.get("key"), "speech-config.json:key")
    required: list[str] = []
    values = {
        "tts_endpoint": _join_endpoint(_safe_text(speech.get("endpoint")), SPEECH_PATH),
        "tts_model": _safe_text(speech.get("tts_model")),
        "default_voice": _safe_text(speech.get("voice")),
        "default_instruction": _safe_text(speech.get("instruction")),
    }
    if enabled:
        for field in ("tts_endpoint", "tts_model", "default_voice"):
            if not values[field]:
                required.append(field)
        if not key:
            required.append("tts_key")
    step = _step("speech", _package(repo, "speech"), enabled, values, {"tts_key": key_report}, required,
                 ["asr_*", "music_*", "yue_*", "yue_token"])
    return step, {"speech.tts_key": key} if key else {}


def _media_plan(repo: Path, config: dict[str, Any], speech: dict[str, Any]) -> tuple[dict[str, Any], dict[str, str]]:
    vision_enabled = bool(_safe_text(config.get("api_key")) and _safe_text(config.get("model")))
    asr_enabled = bool(speech.get("asr_enabled"))
    legacy_backend = _safe_text(speech.get("asr_backend")) or "bailian"
    key_report, api_key = _secret(config.get("api_key"), "config.json:api_key")
    asr_key_report, asr_key = _secret(speech.get("key"), "speech-config.json:key")
    values: dict[str, Any] = {
        "vision_endpoint": _join_endpoint(_safe_text(config.get("base_url")), "/chat/completions"),
        "vision_model": _safe_text(config.get("model")),
        "vision_backend": _safe_text(config.get("provider")) or "deepseek",
        "max_tokens": config.get("max_tokens", 768),
        "thinking": bool(config.get("thinking_enabled")),
        "reasoning_effort": _safe_text(config.get("reasoning_effort")) or "low",
    }
    required: list[str] = []
    profile_id: str | None = None
    if vision_enabled:
        for field in ("vision_endpoint", "vision_model"):
            if not values[field]:
                required.append(field)
        if not api_key:
            required.append("vision_key")
    if asr_enabled:
        if legacy_backend == "local":
            values["asr_backend"] = "local"
            profile_id = _safe_text(speech.get("asr_local_model")) or "sensevoice-small-int8"
            required.append("registered_local_asr_profile:" + profile_id)
        elif legacy_backend == "bailian":
            values.update({"asr_backend": "remote", "asr_endpoint": _join_endpoint(_safe_text(speech.get("endpoint")), ASR_PATH), "asr_model": _safe_text(speech.get("asr_model"))})
            for field in ("asr_endpoint", "asr_model"):
                if not values[field]:
                    required.append(field)
            if not asr_key:
                required.append("asr_key")
        elif legacy_backend == "local_then_bailian":
            required.append("asr_backend:legacy_local_then_bailian_requires_explicit_choice")
        else:
            required.append("asr_backend:unsupported_legacy_value")
    enabled = vision_enabled or asr_enabled
    step = _step("media-understanding", _package(repo, "media-understanding"), enabled, values,
                 {"vision_key": key_report, "asr_key": asr_key_report}, required, profile_id=profile_id)
    secrets: dict[str, str] = {}
    if api_key:
        secrets["media-understanding.vision_key"] = api_key
    if asr_enabled and legacy_backend == "bailian" and asr_key:
        secrets["media-understanding.asr_key"] = asr_key
    return step, secrets


def _official_deepseek(config: dict[str, Any]) -> bool:
    base = _safe_text(config.get("base_url")).rstrip("/")
    return config.get("provider") == "deepseek" and base in {"https://api.deepseek.com", "https://api.deepseek.com/v1"} and bool(_safe_text(config.get("api_key")))


def _web_plan(repo: Path, config: dict[str, Any], mcp: dict[str, Any]) -> dict[str, Any]:
    enabled = bool(mcp.get("enabled", True)) and (bool(mcp.get("web_search_enabled", True)) or bool(mcp.get("web_fetch_enabled", True)))
    required: list[str] = []
    unmapped: list[str] = []
    values = {"legacy_web_search_enabled": bool(mcp.get("web_search_enabled", True)), "legacy_web_fetch_enabled": bool(mcp.get("web_fetch_enabled", True))}
    if _official_deepseek(config):
        required.append("official_deepseek_search_adapter_not_declared_by_web-search_plugin")
        unmapped.extend(["config.json:base_url", "config.json:api_key", "config.json:provider"])
    return _step("web-search", _package(repo, "web-search"), enabled, values, {}, required, unmapped)


def _share_plan(repo: Path, channels: dict[str, Any]) -> tuple[dict[str, Any], dict[str, str]]:
    enabled = bool(channels.get("enabled"))
    cookie_report, cookie = _secret(channels.get("cookie"), "channels-config.json:cookie")
    assistant_report, assistant = _secret(channels.get("assistant_cookie"), "channels-config.json:assistant_cookie")
    required: list[str] = []
    if enabled and not (cookie or assistant):
        required.append("yuanbao_cookie_or_assistant_cookie")
    unmapped = ["channels-config.json:object_bridge_url"] if _safe_text(channels.get("object_bridge_url")) else []
    step = _step("share-reader", _package(repo, "share-reader"), enabled, {},
                 {"yuanbao_cookie": cookie_report, "assistant_cookie": assistant_report}, required, unmapped)
    secrets: dict[str, str] = {}
    if cookie:
        secrets["share-reader.yuanbao_cookie"] = cookie
    if assistant:
        secrets["share-reader.assistant_cookie"] = assistant
    return step, secrets


def _accounts(root: Path, primary_account: str) -> list[tuple[str, Path]]:
    result = [(primary_account, root)]
    path = root / "accounts.json"
    if not path.exists() or path.is_symlink() or not path.is_file():
        return result
    try:
        raw = path.read_bytes()
        entries = json.loads(raw.decode("utf-8-sig")) if len(raw) <= MAX_JSON else []
    except (OSError, UnicodeDecodeError, json.JSONDecodeError):
        return result
    if not isinstance(entries, list):
        return result
    for item in entries:
        if not isinstance(item, dict) or item.get("removed") or not isinstance(item.get("id"), str) or not item["id"] or item["id"] == primary_account:
            continue
        result.append((item["id"], root / "accounts" / _account_dir(item["id"])))
    return result


def build_preview(root: Path, repo: Path, primary_account: str = "primary") -> tuple[dict[str, Any], dict[str, Any]]:
    root = root.resolve()
    repo = repo.resolve()
    report: dict[str, Any] = {"schema_version": SCHEMA_VERSION, "kind": "plugin_migration_preview", "read_only": True, "accounts": []}
    secret_handoff: dict[str, Any] = {"schema_version": SCHEMA_VERSION, "kind": "plugin_migration_host_secrets", "accounts": []}
    for account, directory in _accounts(root, primary_account):
        config, config_error = _read_json(directory / "config.json", required=True)
        speech, speech_error = _read_json(directory / "speech-config.json")
        mcp, mcp_error = _read_json(directory / "mcp-config.json")
        channels, channels_error = _read_json(directory / "channels-config.json")
        if not mcp:
            mcp = {"enabled": True, "web_search_enabled": True, "web_fetch_enabled": True}
        entry: dict[str, Any] = {"account": account, "source_directory": str(directory), "source_errors": {}}
        for name, error in (("config.json", config_error), ("speech-config.json", speech_error), ("mcp-config.json", mcp_error), ("channels-config.json", channels_error)):
            if error:
                entry["source_errors"][name] = error
        media, media_secrets = _media_plan(repo, config, speech)
        speech_step, speech_secrets = _speech_plan(repo, speech)
        share, share_secrets = _share_plan(repo, channels)
        entry["plugins"] = [media, speech_step, _web_plan(repo, config, mcp), share]
        report["accounts"].append(entry)
        all_secrets = {**media_secrets, **speech_secrets, **share_secrets}
        secret_handoff["accounts"].append({"account": account, "secrets": all_secrets})
    return report, secret_handoff


def _inside(parent: Path, child: Path) -> bool:
    try:
        child.relative_to(parent)
        return True
    except ValueError:
        return False


def _validate_new_output(path: Path, root: Path) -> Path:
    path = path.resolve()
    if _inside(root.resolve(), path):
        raise ValueError("output_must_be_outside_source_root")
    if path.exists() or path.is_symlink() or not path.parent.is_dir():
        raise ValueError("output_must_be_new_file_in_existing_directory")
    return path


def _write_new(path: Path, data: Any, root: Path) -> None:
    path = _validate_new_output(path, root)
    raw = json.dumps(data, ensure_ascii=False, indent=2, sort_keys=True).encode("utf-8") + b"\n"
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "wb") as handle:
            handle.write(raw)
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(path, 0o600)
    except BaseException:
        try:
            path.unlink()
        except OSError:
            pass
        raise


def _host_secret_output_allowed() -> bool:
    # chmod(0600) does not install an owner-only DACL on Windows. Until this
    # tool has a verified Windows ACL implementation, exporting credentials is
    # deliberately unavailable there rather than creating an apparently private
    # file readable by inherited ACL principals.
    return os.name != "nt"


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Read-only legacy plugin migration preview")
    parser.add_argument("--admin-root", type=Path, required=True, help="existing administrator configuration root (read only)")
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parents[1], help="repository containing plugin manifests")
    parser.add_argument("--primary-account", default="primary", help="label for the root account; does not alter source data")
    parser.add_argument("--output", type=Path, help="optional NEW public plan JSON outside --admin-root")
    parser.add_argument("--host-secrets-output", type=Path, help="optional NEW 0600 secret handoff JSON outside --admin-root; never commit")
    args = parser.parse_args(argv)
    report, handoff = build_preview(args.admin_root, args.repo, args.primary_account)
    if args.host_secrets_output and not _host_secret_output_allowed():
        raise ValueError("host_secrets_output_requires_verified_private_acl")
    # Validate both targets before publishing either file. A malformed private
    # destination must not leave a misleading "ready" public plan beside it.
    if args.output:
        _validate_new_output(args.output, args.admin_root)
    if args.host_secrets_output:
        _validate_new_output(args.host_secrets_output, args.admin_root)
    if args.output and args.host_secrets_output and args.output.resolve() == args.host_secrets_output.resolve():
        raise ValueError("output_paths_must_differ")
    if args.output:
        _write_new(args.output, report, args.admin_root)
    else:
        json.dump(report, sys.stdout, ensure_ascii=False, indent=2, sort_keys=True)
        sys.stdout.write("\n")
    if args.host_secrets_output:
        _write_new(args.host_secrets_output, handoff, args.admin_root)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (ValueError, OSError) as error:
        print("plugin migration preview failed: " + str(error), file=sys.stderr)
        raise SystemExit(2)
