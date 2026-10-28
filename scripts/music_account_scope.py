"""Account binding for a trusted, token-authenticated managed music broker.

The header is assigned by the management service, never by model arguments.
It supplements the broker token, which may be shared by trusted account workers.
"""
from pathlib import Path
import re

HEADER = "X-Wechat-Agent-Account"


def account_scope(private):
    path = Path(private)
    if path.parent.name != "pri_workspace" or not re.fullmatch(r"[a-f0-9]{64}", path.name):
        raise ValueError("music_account_directory_required")
    return path.name
