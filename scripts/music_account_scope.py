"""Account binding for a trusted, token-authenticated managed music broker.

The header is assigned by the management service, never by model arguments.
It supplements the broker token, which may be shared by trusted account workers.
"""
from pathlib import Path
import re
import hashlib
import json

HEADER = "X-Wechat-Agent-Account"


def account_scope(private):
    path = Path(private)
    if path.parent.name != "pri_workspace":
        raise ValueError("music_account_directory_required")
    if re.fullmatch(r"[a-f0-9]{64}", path.name):
        return path.name  # legacy path, same protocol identity
    identity = path/'account.json'
    try:
        if identity.is_symlink() or identity.stat().st_size > 8192:
            raise ValueError('invalid_account_identity')
        data = json.loads(identity.read_text(encoding='utf-8'))
        account = data['account_id']
        scope = data['scope']
        if not isinstance(account,str) or not account or data['directory'] != path.name or scope != hashlib.sha256(account.encode()).hexdigest():
            raise ValueError('invalid_account_identity')
        return scope
    except (OSError,ValueError,KeyError,TypeError):
        raise ValueError('music_account_directory_required') from None
