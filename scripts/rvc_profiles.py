"""Resolve only registered per-job public voice profiles, never host paths."""
import json
from pathlib import Path
import re


def resolve(public, default, body):
    identity = body.get('voice_profile_id', '')
    if not identity:
        return default
    if not isinstance(identity, str) or not re.fullmatch(r'[a-zA-Z0-9_-]{1,128}', identity) or not public:
        raise ValueError('rvc_voice_profile_not_registered')
    root = Path(public).resolve(strict=True)
    manifest = root / 'manifests' / (identity + '.json')
    if manifest.is_symlink() or not manifest.resolve(strict=True).is_relative_to(root):
        raise ValueError('rvc_voice_manifest_outside_public')
    data = json.loads(manifest.read_text(encoding='utf-8'))
    path = Path(data['configuration'])
    if not path.is_absolute() or path.is_symlink() or not path.resolve(strict=True).is_relative_to(root / 'models' / 'voice-library'):
        raise ValueError('rvc_voice_configuration_outside_library')
    return str(path)
