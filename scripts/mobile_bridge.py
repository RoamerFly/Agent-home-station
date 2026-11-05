"""Compatibility entry point for the mobile-control plugin bridge.

The implementation is packaged with the plugin.  This fixed relative loader
keeps existing administrators, tests, and service definitions working without
accepting an arbitrary module or runtime path.
"""
import importlib.util
from pathlib import Path


_IMPLEMENTATION = Path(__file__).resolve().parents[1] / "plugins" / "mobile-control" / "runtime" / "bridge.py"
_SPEC = importlib.util.spec_from_file_location("mobile_control_bridge", _IMPLEMENTATION)
if _SPEC is None or _SPEC.loader is None:
    raise RuntimeError("mobile_bridge_implementation_unavailable")
_MODULE = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(_MODULE)

# Re-export the established test/import surface.  In particular, aliases such
# as `mobile_bridge.socket` point at the same module object used by Bridge, so
# existing monkeypatches retain their original effect.
for _name, _value in vars(_MODULE).items():
    if not _name.startswith("__"):
        globals()[_name] = _value


if __name__ == "__main__":
    main()
