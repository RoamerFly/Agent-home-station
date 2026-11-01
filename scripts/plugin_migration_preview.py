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
import ipaddress
import json
import os
from pathlib import Path
import sys
from typing import Any
from urllib.parse import urlsplit


SCHEMA_VERSION = 1
MAX_JSON = 1 << 20
SPEECH_PATH = "/api/v1/services/audio/tts/SpeechSynthesizer"
ASR_PATH = "/api/v1/services/aigc/multimodal-generation/generation"
MUSIC_PATH = "/api/v1/services/audio/music/generation"
AUDIO_REVIEW_PATH = "/compatible-mode/v1/chat/completions"
AUDIO_REVIEW_MODEL = "qwen3.8-omni-flash"
PROJECT_DEVELOPMENT_PERMISSIONS = {"workspace.read", "workspace.write", "process.execute"}
EXTERNAL_SYNC_PERMISSION = "network"


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


def _source_hash(path: Path) -> tuple[str, str | None]:
    """Return a report-safe source fingerprint without exporting its bytes.

    In particular plugins.json can contain endpoint templates and credentials.
    Its hash is an approval boundary only; it is never parsed into public plan
    values beyond the enablement/async classification below.
    """
    if not path.exists():
        return _sha256(b"missing"), "missing"
    if path.is_symlink() or not path.is_file():
        return _sha256(b"not_regular"), "not_regular"
    try:
        data = path.read_bytes()
    except OSError:
        return _sha256(b"unreadable"), "unreadable"
    if len(data) > MAX_JSON:
        return _sha256(b"too_large"), "too_large"
    return _sha256(data), None


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
        # Store.Prepare uses the exact plugin.json bytes as its package digest.
        # Do not derive a digest from re-serialized JSON here.
        "expected_package_digest": _sha256(manifest.read_bytes()) if not error else None,
        "permissions": value.get("permissions") if isinstance(value.get("permissions"), list) and all(isinstance(permission, str) for permission in value["permissions"]) else [],
    }
    if error:
        out["manifest_error"] = error
    return out


def _step(plugin_id: str, package: dict[str, Any], legacy_enabled: bool, values: dict[str, Any], secrets: dict[str, Any], required_missing: list[str], unmapped: list[str] | None = None, profile_id: str | None = None, resource_proof: dict[str, Any] | None = None) -> dict[str, Any]:
    status = "ready_for_administrator_review"
    if package["status"] != "ready" or required_missing:
        status = "blocked"
    result: dict[str, Any] = {
        "plugin": plugin_id,
        "package": package,
        "permissions": package.get("permissions", []),
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
    if resource_proof:
        result["resource_proof"] = resource_proof
    return result


def _declares_named_speech(repo: Path) -> bool:
    """Check the package contract, never infer local support from a voice name."""
    package, package_error = _read_json(repo / "plugins" / "speech" / "plugin.json", required=True)
    schema, schema_error = _read_json(repo / "plugins" / "speech" / "config.schema.json", required=True)
    capabilities, capabilities_error = _read_json(repo / "plugins" / "speech" / "capabilities.json", required=True)
    if package_error or schema_error or capabilities_error or package.get("status") != "ready":
        return False
    if not {"artifact.read", "model.invoke"}.issubset(set(package.get("permissions", []))):
        return False
    properties = schema.get("properties")
    profile = properties.get("local_profile_id") if isinstance(properties, dict) else None
    if not isinstance(profile, dict) or profile.get("type") != "string" or "gpt-sovits" not in profile.get("enum", []):
        return False
    rows = capabilities.get("capabilities")
    return isinstance(rows, list) and any(
        isinstance(item, dict)
        and item.get("id") == "speech.synthesize_named"
        and item.get("implementation") == "ready"
        and {"artifact.read", "model.invoke"}.issubset(set(item.get("permissions", [])))
        for item in rows
    )


def _declares_local_music_bridge(repo: Path) -> bool:
    """Require the local bridge's exact configuration and ready capability."""
    package, package_error = _read_json(repo / "plugins" / "music" / "plugin.json", required=True)
    schema, schema_error = _read_json(repo / "plugins" / "music" / "config.schema.json", required=True)
    capabilities, capabilities_error = _read_json(repo / "plugins" / "music" / "capabilities.json", required=True)
    if package_error or schema_error or capabilities_error or package.get("status") != "ready":
        return False
    if "network" not in package.get("permissions", []):
        return False
    properties = schema.get("properties")
    endpoint = properties.get("local_endpoint") if isinstance(properties, dict) else None
    token = properties.get("local_token") if isinstance(properties, dict) else None
    if not isinstance(endpoint, dict) or endpoint.get("type") != "string" or endpoint.get("x-secret") or endpoint.get("x-host-only"):
        return False
    if not isinstance(token, dict) or token.get("type") != "string" or not token.get("x-secret") or not token.get("x-host-only"):
        return False
    rows = capabilities.get("capabilities")
    return isinstance(rows, list) and any(
        isinstance(item, dict)
        and item.get("id") == "music.bridge_request"
        and item.get("implementation") == "ready"
        and "network" in item.get("permissions", [])
        for item in rows
    )


def _declares_project_development(repo: Path) -> bool:
    """Accept only the reviewed empty-config workspace/project bridge.

    The legacy agent host remains the authority for roots, isolation and
    process profiles.  This offline preview therefore validates the package
    declaration but never tries to derive, copy or expose those controls.
    """
    package, package_error = _read_json(repo / "plugins" / "project-development" / "plugin.json", required=True)
    schema, schema_error = _read_json(repo / "plugins" / "project-development" / "config.schema.json", required=True)
    capabilities, capabilities_error = _read_json(repo / "plugins" / "project-development" / "capabilities.json", required=True)
    if package_error or schema_error or capabilities_error or package.get("id") != "project-development" or package.get("status") != "ready":
        return False
    if not PROJECT_DEVELOPMENT_PERMISSIONS.issubset(set(package.get("permissions", []))):
        return False
    if schema.get("type") != "object" or schema.get("properties") != {} or schema.get("additionalProperties") is not False:
        return False
    rows = capabilities.get("capabilities")
    if not isinstance(rows, list):
        return False
    required = {
        "project.workspace_read": "workspace.read",
        "project.workspace_write": "workspace.write",
        "project.workspace_execute": "process.execute",
    }
    declared = {
        item.get("id"): set(item.get("permissions", []))
        for item in rows
        if isinstance(item, dict) and item.get("implementation") == "ready"
    }
    return all(permission in declared.get(capability, set()) for capability, permission in required.items())


def _declares_external_capability(repo: Path, capability_id: str) -> bool:
    """Require one exact empty-config external adapter capability.

    This is deliberately not a general conversion for old MCP servers or
    background jobs.  They remain host-owned until their own reviewed adapter
    exists, rather than being silently represented as a one-shot HTTP call.
    """
    package, package_error = _read_json(repo / "plugins" / "external-tools" / "plugin.json", required=True)
    schema, schema_error = _read_json(repo / "plugins" / "external-tools" / "config.schema.json", required=True)
    capabilities, capabilities_error = _read_json(repo / "plugins" / "external-tools" / "capabilities.json", required=True)
    if package_error or schema_error or capabilities_error or package.get("id") != "external-tools" or package.get("status") != "ready":
        return False
    if EXTERNAL_SYNC_PERMISSION not in package.get("permissions", []):
        return False
    if schema.get("type") != "object" or schema.get("properties") != {} or schema.get("additionalProperties") is not False:
        return False
    rows = capabilities.get("capabilities")
    return isinstance(rows, list) and any(
        isinstance(item, dict)
        and item.get("id") == capability_id
        and item.get("implementation") == "ready"
        and item.get("discoverable") is True
        and item.get("async") is not True
        and EXTERNAL_SYNC_PERMISSION in item.get("permissions", [])
        for item in rows
    )


def _classify_external_mcp(server: Any) -> str:
    """Classify one enabled MCP declaration without exposing its connection.

    HTTP and streamable HTTP use the reviewed MCP adapter. Stdio only counts
    when it names an already registered plugin runtime and has no raw launch
    data. A command/args/env descriptor is never a migration-time execution
    grant.
    """
    if not isinstance(server, dict):
        return "unsupported"
    transport = _safe_text(server.get("transport")).strip().lower()
    if not transport and _safe_text(server.get("plugin_id")):
        transport = "stdio"
    if transport in {"http", "streamable-http"}:
        return "mcp"
    if transport == "stdio":
        if _safe_text(server.get("plugin_id")) and not _safe_text(server.get("command")) and not server.get("args") and not server.get("env"):
            return "mcp"
        return "raw_stdio"
    return "unsupported"


def _external_legacy_intent(mcp: dict[str, Any], ledger: dict[str, Any]) -> dict[str, bool]:
    """Classify only legacy lifecycle shape, never protocol data.

    The old ledger remains the only source of service address, request
    template and credential data.  This reads only the public structural flags
    required to avoid claiming MCP or async declarations migrated.
    """
    if not bool(mcp.get("enabled", True)):
        return {"declared": False, "sync": False, "job": False, "poll": False, "mcp": False, "raw_stdio": False, "unsupported": False, "mcp_config": False, "plugin_ledger": False}
    intent = {"declared": False, "sync": False, "job": False, "poll": False, "mcp": False, "raw_stdio": False, "unsupported": False, "mcp_config": False, "plugin_ledger": False}
    servers = mcp.get("servers")
    if isinstance(servers, list):
        for server in servers:
            if not isinstance(server, dict) or not server.get("enabled"):
                continue
            kind = _classify_external_mcp(server)
            intent["declared"] = True
            intent["mcp_config"] = True
            intent[kind] = True
    for entry in ledger.values():
        if not isinstance(entry, dict) or entry.get("removed") or not entry.get("enabled"):
            continue
        manifest = entry.get("manifest")
        if not isinstance(manifest, dict):
            continue
        if isinstance(manifest.get("mcp"), dict):
            intent["declared"] = True
            intent["plugin_ledger"] = True
            intent[_classify_external_mcp(manifest["mcp"])] = True
        tools = manifest.get("tools")
        if not isinstance(tools, list):
            continue
        for tool in tools:
            if not isinstance(tool, dict):
                continue
            intent["declared"] = True
            intent["plugin_ledger"] = True
            if tool.get("job") is None and tool.get("poll") is None:
                intent["sync"] = True
            elif tool.get("job") is not None and tool.get("poll") is not None:
                intent["job"] = True
            elif tool.get("job") is None and tool.get("poll") is not None:
                intent["poll"] = True
            else:
                intent["unsupported"] = True
    return intent


def _external_tools_plan(repo: Path, mcp: dict[str, Any], ledger: dict[str, Any]) -> tuple[dict[str, Any], dict[str, str]]:
    """Prepare only reviewed sync or start/poll/cancel external adapters.

    No legacy endpoint, header, template, MCP setup or secret can enter either
    public values or the host-secret handoff.  The host keeps plugins.json as
    the sole authority for those records.
    """
    intent = _external_legacy_intent(mcp, ledger)
    sync_declared = _declares_external_capability(repo, "external.http_request")
    job_declared = _declares_external_capability(repo, "external.job_request")
    poll_declared = _declares_external_capability(repo, "external.poll_request")
    mcp_declared = _declares_external_capability(repo, "external.mcp_request")
    legacy_enabled = (intent["sync"] and sync_declared) or (intent["job"] and job_declared) or (intent["poll"] and poll_declared) or (intent["mcp"] and mcp_declared)
    required: list[str] = []
    if intent["declared"] and not legacy_enabled:
        required.append("no_matching_supported_external_tools")
    if intent["sync"] and not sync_declared:
        required.append("official_sync_external_adapter_not_declared_by_external_tools_plugin")
    if intent["job"] and not job_declared:
        required.append("official_external_job_adapter_not_declared_by_external_tools_plugin")
    if intent["poll"] and not poll_declared:
        required.append("official_external_poll_adapter_not_declared_by_external_tools_plugin")
    if intent["mcp"] and not mcp_declared:
        required.append("official_external_mcp_adapter_not_declared_by_external_tools_plugin")
    retained = []
    if intent["plugin_ledger"]:
        retained.append("plugins.json:host_retained_declarations_and_credentials")
    if intent["mcp_config"]:
        retained.append("mcp-config.json:host_retained_connections_and_credentials")
    if intent["job"] or intent["poll"]:
        retained.append("legacy_external_tasks:origin_and_configuration_proof_required")
    if intent["raw_stdio"]:
        retained.append("mcp_stdio_runtime_package_required")
    if intent["unsupported"]:
        retained.append("external_declarations:unsupported_runtime_retained")
    return _step("external-tools", _package(repo, "external-tools"), legacy_enabled, {}, {}, required, retained), {}


def _safe_local_music_endpoint(raw: str) -> str | None:
    """Mirror localMusicEndpoint: only a root HTTPS or local/private HTTP URL."""
    if not raw or len(raw) > 4096 or any(character.isspace() for character in raw) or "\\" in raw or "\x00" in raw or "?" in raw or "#" in raw:
        return None
    try:
        parsed = urlsplit(raw)
        host = parsed.hostname
        # Access port as well: urlsplit otherwise tolerates malformed ports.
        _ = parsed.port
    except ValueError:
        return None
    if parsed.scheme not in {"http", "https"} or not host or parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path.rstrip("/"):
        return None
    if parsed.scheme == "https":
        return raw.rstrip("/")
    if host.lower() == "localhost":
        return raw.rstrip("/")
    try:
        address = ipaddress.ip_address(host)
    except ValueError:
        return None
    return raw.rstrip("/") if address.is_loopback or address.is_private else None


def _pending_named_speech_manifest(root: Path) -> dict[str, Any] | None:
    """Find only an explicit managed manifest marker, not a voice/model file.

    Python's offline report has none of the host path/authorization checks.  A
    marker therefore becomes a blocked host-verification requirement, never a
    claim that a named voice is usable.
    """
    path = root / "managed-workspaces" / "public_workspace" / "manifests" / "gpt-sovits.json"
    if not path.exists() or path.is_symlink() or not path.is_file():
        return None
    _, error = _read_json(path, required=True)
    if error:
        return None
    return {"source": "gpt-sovits-manifest", "present": True, "host_verification": "required"}


def _speech_plan(repo: Path, root: Path, speech: dict[str, Any]) -> tuple[dict[str, Any], dict[str, str]]:
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
    profile_id: str | None = None
    proof = _pending_named_speech_manifest(root)
    if proof and _declares_named_speech(repo):
        profile_id = "gpt-sovits"
        values["local_profile_id"] = profile_id
        # Only the Go host can pin managed paths, grants and resource bytes.
        # Keep this report blocked until that independent proof is obtained.
        required.append("registered_named_speech_profile:gpt-sovits:host_verification_required")
        enabled = True
    step = _step("speech", _package(repo, "speech"), enabled, values, {"tts_key": key_report}, required,
                 ["asr_*", "music_*", "yue_*", "yue_token"], profile_id=profile_id, resource_proof=proof)
    return step, {"speech.tts_key": key} if key else {}


def _music_plan(repo: Path, speech: dict[str, Any]) -> tuple[dict[str, Any], dict[str, str]]:
    cloud_enabled = bool(speech.get("music_enabled"))
    local_enabled = bool(speech.get("yue_enabled"))
    local_endpoint = _safe_text(speech.get("yue_endpoint"))
    local_configured = local_enabled or bool(local_endpoint) or bool(_safe_text(speech.get("yue_token")))
    key_report, key = _secret(speech.get("key"), "speech-config.json:key")
    local_token_report, local_token = _secret(speech.get("yue_token"), "speech-config.json:yue_token")
    values = {
        "music_endpoint": _join_endpoint(_safe_text(speech.get("endpoint")), MUSIC_PATH),
        "music_model": _safe_text(speech.get("music_model")),
    }
    required = ["music_key"] if cloud_enabled and not key and not local_enabled else []
    secrets = {"music_key": key_report, "local_token": local_token_report}
    handoff = {"music.music_key": key} if key else {}
    if local_configured:
        if not _declares_local_music_bridge(repo):
            required.append("official_local_music_bridge_not_declared_by_music_plugin")
        elif not local_endpoint:
            required.append("local_endpoint")
        else:
            safe_endpoint = _safe_local_music_endpoint(local_endpoint)
            if not safe_endpoint:
                required.append("local_endpoint_invalid")
            else:
                values["local_endpoint"] = safe_endpoint
                if local_token:
                    handoff["music.local_token"] = local_token
                if local_enabled and not local_token:
                    required.append("local_token")
    step = _step("music", _package(repo, "music"), cloud_enabled or local_enabled, values, secrets, required,
                 ["tts_*", "asr_*", "named_voice_library", "rvc_profiles"])
    return step, handoff


def _project_development_plan(repo: Path, agent: dict[str, Any]) -> tuple[dict[str, Any], dict[str, str]]:
    """Describe legacy workspace intent without transferring host authority.

    A ready project-development package exposes read/write/execute together.
    Old `read` or `files` host modes therefore cannot be silently converted to
    it: the administrator must explicitly review the expanded package grant.
    Roots and platform/process profiles remain in host-owned state and are
    intentionally neither inspected nor put into this plan.
    """
    host = agent.get("host") if isinstance(agent.get("host"), dict) else {}
    mode = _safe_text(host.get("mode"))
    legacy_enabled = bool(agent.get("enabled")) and mode == "full"
    required: list[str] = []
    if bool(agent.get("enabled")):
        if not _declares_project_development(repo):
            required.append("official_project_development_adapter_not_declared_by_plugin")
        elif mode != "full":
            required.append("explicit_project_permissions_review_required")
    unmapped = [
        "agent-config.json:host.roots",
        "agent-config.json:host.isolation",
        "agent-config.json:host.windows",
        "agent-config.json:host.timeout_seconds",
        "agent-config.json:host.output_bytes",
    ]
    return _step("project-development", _package(repo, "project-development"), legacy_enabled, {}, {}, required, unmapped), {}


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
            values["local_asr_profile_id"] = profile_id
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
    media_secrets: dict[str, Any] = {"vision_key": key_report, "asr_key": asr_key_report}
    handoff: dict[str, str] = {}
    if api_key:
        handoff["media-understanding.vision_key"] = api_key
    if asr_enabled and legacy_backend == "bailian" and asr_key:
        handoff["media-understanding.asr_key"] = asr_key
    # Legacy ReviewAudio used the existing regional root, credential and fixed
    # Omni Flash model. It is now an optional media.listen route, not evidence
    # that media-understanding was previously enabled.
    if asr_key:
        audio_key_report, audio_key = _secret(speech.get("key"), "speech-config.json:key")
        values.update({"audio_review_endpoint": _join_endpoint(_safe_text(speech.get("endpoint")), AUDIO_REVIEW_PATH), "audio_review_model": AUDIO_REVIEW_MODEL})
        media_secrets["audio_review_key"] = audio_key_report
        if audio_key:
            handoff["media-understanding.audio_review_key"] = audio_key
    enabled = vision_enabled or asr_enabled
    step = _step("media-understanding", _package(repo, "media-understanding"), enabled, values,
                 media_secrets, required, profile_id=profile_id)
    return step, handoff


def _official_deepseek(config: dict[str, Any]) -> bool:
    base = _safe_text(config.get("base_url")).rstrip("/")
    return config.get("provider") == "deepseek" and base in {"https://api.deepseek.com", "https://api.deepseek.com/v1"} and bool(_safe_text(config.get("api_key")))


def _declares_deepseek_search(repo: Path) -> bool:
    """Require all three declarations; never infer paid support from a name."""
    package, package_error = _read_json(repo / "plugins" / "web-search" / "plugin.json", required=True)
    schema, schema_error = _read_json(repo / "plugins" / "web-search" / "config.schema.json", required=True)
    caps, caps_error = _read_json(repo / "plugins" / "web-search" / "capabilities.json", required=True)
    if package_error or schema_error or caps_error or package.get("status") != "ready":
        return False
    properties = schema.get("properties")
    backend = properties.get("search_backend") if isinstance(properties, dict) else None
    key = properties.get("search_key") if isinstance(properties, dict) else None
    if not isinstance(backend, dict) or "deepseek" not in backend.get("enum", []) or not isinstance(key, dict) or key.get("type") != "string" or key.get("x-secret") is not True or key.get("x-host-only") is not True:
        return False
    capabilities = caps.get("capabilities")
    if not isinstance(capabilities, list) or not any(isinstance(cap, dict) and cap.get("id") == "web.search" and cap.get("implementation") == "ready" and "network" in cap.get("permissions", []) for cap in capabilities):
        return False
    routes = package.get("http_routes")
    return isinstance(routes, list) and any(isinstance(route, dict) and route.get("id") == "deepseek-search" and route.get("endpoint") == "https://api.deepseek.com/v1/tools/search" and route.get("credential") == "search_key" and route.get("credential_header") == "Authorization" and route.get("credential_prefix") == "Bearer " and "web.search" in route.get("capabilities", []) for route in routes)


def _web_plan(repo: Path, config: dict[str, Any], mcp: dict[str, Any]) -> tuple[dict[str, Any], dict[str, str]]:
    enabled = bool(mcp.get("enabled", True)) and (bool(mcp.get("web_search_enabled", True)) or bool(mcp.get("web_fetch_enabled", True)))
    required: list[str] = []
    unmapped: list[str] = []
    values = {"legacy_web_search_enabled": bool(mcp.get("web_search_enabled", True)), "legacy_web_fetch_enabled": bool(mcp.get("web_fetch_enabled", True))}
    secrets: dict[str, Any] = {}
    handoff: dict[str, str] = {}
    if _official_deepseek(config):
        if _declares_deepseek_search(repo):
            values["search_backend"] = "deepseek"
            search_key, raw_key = _secret(config.get("api_key"), "config.json:api_key")
            secrets["search_key"] = search_key
            if raw_key:
                handoff["web-search.search_key"] = raw_key
            else:
                required.append("search_key")
        else:
            required.append("official_deepseek_search_adapter_not_declared_by_web-search_plugin")
            unmapped.extend(["config.json:base_url", "config.json:api_key", "config.json:provider"])
    return _step("web-search", _package(repo, "web-search"), enabled, values, secrets, required, unmapped), handoff


def _declares_share_object_bridge(repo: Path) -> bool:
    """Accept only an ordinary, explicit share-reader bridge setting."""
    package, package_error = _read_json(repo / "plugins" / "share-reader" / "plugin.json", required=True)
    schema, schema_error = _read_json(repo / "plugins" / "share-reader" / "config.schema.json", required=True)
    if package_error or schema_error or package.get("id") != "share-reader":
        return False
    properties = schema.get("properties")
    field = properties.get("object_bridge_url") if isinstance(properties, dict) else None
    return isinstance(field, dict) and field.get("type") == "string" and field.get("x-secret") is not True and field.get("x-host-only") is not True


def _loopback_object_bridge_url(raw: Any) -> str | None:
    """Mirror the Go loopback-only bridge contract without contacting it."""
    value = _safe_text(raw)
    try:
        parsed = urlsplit(value)
        port = parsed.port
    except ValueError:
        return None
    if parsed.scheme != "http" or parsed.username is not None or parsed.password is not None or parsed.hostname not in {"127.0.0.1", "::1"} or port is None or not 1 <= port <= 65535 or parsed.query or parsed.fragment or parsed.path not in {"", "/"}:
        return None
    return value[:-1] if parsed.path == "/" else value


def _share_plan(repo: Path, channels: dict[str, Any]) -> tuple[dict[str, Any], dict[str, str]]:
    enabled = bool(channels.get("enabled"))
    cookie_report, cookie = _secret(channels.get("cookie"), "channels-config.json:cookie")
    assistant_report, assistant = _secret(channels.get("assistant_cookie"), "channels-config.json:assistant_cookie")
    required: list[str] = []
    if enabled and not (cookie or assistant):
        required.append("yuanbao_cookie_or_assistant_cookie")
    values: dict[str, Any] = {}
    unmapped: list[str] = []
    legacy_bridge = _safe_text(channels.get("object_bridge_url"))
    if legacy_bridge:
        bridge = _loopback_object_bridge_url(legacy_bridge)
        if bridge is None:
            required.append("object_bridge_url_invalid")
            unmapped.append("channels-config.json:object_bridge_url")
        elif not _declares_share_object_bridge(repo):
            required.append("object_bridge_url_not_declared_by_share_reader_plugin")
            unmapped.append("channels-config.json:object_bridge_url")
        else:
            values["object_bridge_url"] = bridge
            # Legacy code could try the bridge and then fall back to the
            # Yuanbao cookie. The plugin resolver is stricter: preserve the
            # data but require an administrator to choose that policy.
            if cookie:
                required.append("explicit_bridge_fallback_policy_needed")
    step = _step("share-reader", _package(repo, "share-reader"), enabled, values,
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
        agent, agent_error = _read_json(directory / "agent-config.json")
        legacy_plugins, plugins_error = _read_json(directory / "plugins.json")
        if not mcp:
            mcp = {"enabled": True, "web_search_enabled": True, "web_fetch_enabled": True}
        source_paths = {
            "config_json": directory / "config.json",
            "speech_config_json": directory / "speech-config.json",
            "mcp_config_json": directory / "mcp-config.json",
            "channels_config_json": directory / "channels-config.json",
            "agent_config_json": directory / "agent-config.json",
            "plugins_json": directory / "plugins.json",
        }
        source_hashes = {name: _source_hash(path)[0] for name, path in source_paths.items()}
        entry: dict[str, Any] = {"account": account, "source_directory": str(directory), "source_hashes": source_hashes, "source_errors": {}}
        for name, error in (("config.json", config_error), ("speech-config.json", speech_error), ("mcp-config.json", mcp_error), ("channels-config.json", channels_error), ("agent-config.json", agent_error), ("plugins.json", plugins_error)):
            if error:
                entry["source_errors"][name] = error
        media, media_secrets = _media_plan(repo, config, speech)
        speech_step, speech_secrets = _speech_plan(repo, root, speech)
        music, music_secrets = _music_plan(repo, speech)
        project_development, project_secrets = _project_development_plan(repo, agent)
        share, share_secrets = _share_plan(repo, channels)
        web, web_secrets = _web_plan(repo, config, mcp)
        external, external_secrets = _external_tools_plan(repo, mcp, legacy_plugins)
        entry["plugins"] = [media, speech_step, music, project_development, web, share, external]
        report["accounts"].append(entry)
        all_secrets = {**media_secrets, **speech_secrets, **music_secrets, **project_secrets, **web_secrets, **share_secrets, **external_secrets}
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
