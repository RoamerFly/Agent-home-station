"""Read-only Windows candidate state check.

This helper never starts a process, opens a login window, changes grants, or
prints credentials/content. Run it after the owner has manually logged in to
the development package.
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.request import Request, build_opener, ProxyHandler, urlopen


def native_read(data: Path, path: str) -> dict:
    token_path = data / "api-token"
    if not token_path.is_file():
        return {"available": False, "reason": "api-token-not-found"}
    token = token_path.read_text(encoding="utf-8").strip()
    if not token:
        return {"available": False, "reason": "api-token-empty"}
    request = Request(
        "http://127.0.0.1:6174" + path,
        headers={"Authorization": "Bearer " + token},
    )
    opener = build_opener(ProxyHandler({}))
    try:
        with opener.open(request, timeout=8) as response:
            value = json.load(response)
    except HTTPError as error:
        return {"available": False, "http_status": error.code}
    except (OSError, URLError, TimeoutError) as error:
        return {"available": False, "error_type": type(error).__name__}
    return value if isinstance(value, dict) else {"available": False, "reason": "invalid-response"}


def douyin_state(data: Path) -> dict:
    root = data / "apps" / "douyin" / "web-login"
    files = sorted(root.glob("fish-*/login.json"))
    if not files:
        return {"available": False, "profiles": 0}
    rows = []
    for path in files:
        try:
            value = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, ValueError):
            rows.append({"valid": False})
            continue
        rows.append(
            {
                "valid": True,
                "state": value.get("state"),
                "paused": value.get("paused") is True,
                "detached": value.get("detached") is True,
                "confirmed": bool(value.get("confirmed_at")),
                "read_enabled": value.get("read_enabled") is True,
                "publish_enabled": value.get("publish_enabled") is True,
            }
        )
    return {"available": True, "profiles": len(rows), "items": rows}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--package", required=True, type=Path)
    args = parser.parse_args()
    package = args.package.resolve()
    data = package / "data" / "station"
    status = native_read(data, "/v1/status")
    account = native_read(data, "/v1/accounts/desktop")
    native = {
        "available": status.get("available", False),
        "state": status.get("state"),
        "native_login": status.get("nativeLogin"),
        "account": account.get("id"),
        "authenticated": account.get("authenticated"),
        "connection": account.get("connection"),
        "profile_recovery_pending": account.get("profileRecoveryPending"),
    }
    print(
        json.dumps(
            {
                "package": str(package),
                "package_exists": package.is_dir(),
                "native": native,
                "douyin": douyin_state(data),
                "actions_performed": [],
            },
            ensure_ascii=False,
            indent=2,
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
