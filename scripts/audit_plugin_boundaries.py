"""Read-only migration inventory and final lightweight-core gate.

This scans only product source that currently owns optional services. It does
not read account data, runtimes, models, or pending cleanup. ``--require-light``
is a release gate; normal output is an actionable audit.
"""
import argparse
import json
import re
import subprocess
from collections import defaultdict
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SOURCE_ROOTS = (ROOT / "internal" / "admin", ROOT / "internal" / "bailian",
                ROOT / "internal" / "mobile", ROOT / "internal" / "agenthost",
                ROOT / "internal" / "channels", ROOT / "internal" / "mcptools")

# Native WeChat send, media encoding/staging, core memory, account auth and the
# generic plugin host remain in the lightweight core. Expressions describe
# implementation ownership, not every harmless mention of a capability name.
SERVICE_MARKERS = {
    "speech": (r"bailian\.New\(", r"speech_synthesize", r"voice_synthesis", r"gpt.?sovits", r"voice_library_"),
    "music": (r"(?<![A-Za-z])YuE(?![A-Za-z])", r"(?<![A-Za-z])RVC(?![A-Za-z])", r"music_(?:local|rvc|service|reference|sampling|resume)"),
    "media-understanding": (r"mediaUnderstanding", r"sampleVideo\(", r"video_inspect", r"transcribeAudio\("),
    "share-reader": (r"channels\.ResolveObject", r"ReadPublicShare", r"resolveReceivedChannels", r"ChannelsShareURL"),
    "mobile-control": (r"\.mobile\b", r"mobileRoutes\(", r"mobile\.Task", r"mobile\.Settings"),
    "project-development": (r"agenthost\.", r"workspaceRoutes\(", r"projectTools\(", r"server_(?:exec|process|windows_process)"),
    "web-search": (r"mcptools\.(?:Search|Read|Fetch)", r"WebFetchEnabled", r"web_search|web_fetch"),
    "visual-generation": (r"comfy", r"visual_generate", r"image_generation"),
    "external-tools": (r"external__tool", r"external[_-]tool", r"mcp\."),
}
DIRECT_DEPS = {
    "wechat-bot/internal/bailian": "speech",
    "wechat-bot/internal/mobile": "mobile-control",
    "wechat-bot/internal/agenthost": "project-development",
    "wechat-bot/internal/channels": "share-reader",
}
PLUGIN_IMPLEMENTATION_PREFIX = "wechat-bot/plugins/"

def category(path):
    name = path.name
    if name.endswith("_tools.go") or name in {"agent_tools.go", "service_plugin_tools.go"}:
        return "tool"
    if name.endswith("_settings.go") or name in {"manager.go", "storage.go", "mcp.go"}:
        return "configuration"
    if "workspace" in name or "runtime" in name or "process" in name:
        return "runtime"
    if name in {"agent_workspace.go", "capabilities.go"}:
        return "prompt"
    if name in {"server.go", "agent_control.go", "mobile.go"}:
        return "route"
    return "implementation"

def source_hits():
    result = defaultdict(list)
    compiled = {plugin: [re.compile(pattern, re.I) for pattern in patterns]
                for plugin, patterns in SERVICE_MARKERS.items()}
    for root in SOURCE_ROOTS:
        if not root.exists():
            continue
        for path in root.rglob("*.go"):
            if path.name.endswith("_test.go"):
                continue
            text = path.read_text(encoding="utf-8", errors="replace")
            relative = path.relative_to(ROOT).as_posix()
            for plugin, patterns in compiled.items():
                markers = [pattern.pattern for pattern in patterns if pattern.search(text)]
                if markers:
                    result[plugin].append({"path": relative, "kind": category(path), "markers": markers})
    return {plugin: sorted(rows, key=lambda row: (row["kind"], row["path"]))
            for plugin, rows in sorted(result.items())}

def plugin_inventory():
    result = []
    for manifest in sorted((ROOT / "plugins").glob("*/plugin.json")):
        package = json.loads(manifest.read_text(encoding="utf-8"))
        doc = json.loads(manifest.with_name("capabilities.json").read_text(encoding="utf-8"))
        capabilities = [{"id": item["id"], "implementation": item.get("implementation"),
                         "discoverable": bool(item.get("discoverable"))}
                        for item in doc.get("capabilities", [])]
        result.append({"id": package["id"], "status": package.get("status"),
                       "runtime": package.get("runtime", {}).get("entrypoint", ""),
                       "capabilities": capabilities})
    return result

def static_plugin_implementation_dependencies(dependencies):
    """Return plugin implementation packages compiled into the core binary.

    Package descriptors under ``plugins/`` are catalog input and are not Go
    dependencies.  A dependency whose import path has a component after the
    repository ``plugins/`` prefix, however, means the admin binary directly
    links plugin-specific implementation code.  Keep this separate from the
    legacy ``internal/*`` residual list: both prevent a lightweight-core
    release, but they require different migration work.
    """
    result = []
    for package in sorted(set(dependencies)):
        if not package.startswith(PLUGIN_IMPLEMENTATION_PREFIX):
            continue
        relative = package.removeprefix(PLUGIN_IMPLEMENTATION_PREFIX)
        parts = relative.split("/")
        if not parts or not parts[0]:
            continue
        result.append({"package": package, "target_plugin": parts[0]})
    return result

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--require-light", action="store_true")
    parser.add_argument("--compact", action="store_true")
    args = parser.parse_args()
    deps = set(subprocess.run(["go", "list", "-deps", "./cmd/wechat-admin"], cwd=ROOT,
                              text=True, capture_output=True, check=True).stdout.splitlines())
    remaining_deps = [{"package": package, "target_plugin": plugin}
                      for package, plugin in DIRECT_DEPS.items() if package in deps]
    static_plugin_deps = static_plugin_implementation_dependencies(deps)
    inventory, hits = plugin_inventory(), source_hits()
    plugins = {item["id"]: {"status": item["status"], "runtime": item["runtime"],
                             "capabilities": item["capabilities"], "core_references": hits.get(item["id"], [])}
               for item in inventory}
    complete = (len(inventory) == 9 and all(item["status"] == "ready" for item in inventory)
                and not remaining_deps and not static_plugin_deps and not any(hits.values()))
    report = {"light_core_ready": complete, "audit_only": True,
              "direct_optional_dependencies": remaining_deps,
              "static_plugin_implementation_dependencies": static_plugin_deps,
              "plugins": plugins,
              "totals": {"plugins": len(inventory), "ready_plugins": sum(item["status"] == "ready" for item in inventory),
                         "core_optional_source_files": sum(len(rows) for rows in hits.values()),
                         "core_optional_dependency_packages": len(remaining_deps),
                         "core_static_plugin_implementation_packages": len(static_plugin_deps)},
              "core_boundary": {"retained": ["native_wechat_login_and_send", "account_and_auth", "memory_and_context", "asset_identity_and_delivery", "media_encoding_and_staging", "plugin_host"],
                                "excluded_from_scan": ["tests", "account_data", "models", "runtimes", "pending_cleanup"]}}
    if args.compact:
        for item in report["plugins"].values():
            item["core_references"] = [{"path": row["path"], "kind": row["kind"]} for row in item["core_references"]]
    print(json.dumps(report, ensure_ascii=False, indent=2))
    return 1 if args.require_light and not complete else 0

if __name__ == "__main__":
    raise SystemExit(main())
