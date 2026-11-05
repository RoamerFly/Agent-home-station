"""Load pinned singing parameters after a verified library folder relocation.

The config bytes (and therefore existing job fingerprint) remain unchanged.
Only missing RVC weight/index paths can relocate beside this exact config,
and only when their pinned SHA256 matches. No disk search or default voice.
"""
import hashlib
import json
from pathlib import Path


def load_singing_config(file,data=None):
    file=Path(file)
    config=json.loads(file.read_text(encoding='utf-8-sig') if data is None else data.decode('utf-8-sig'))
    if config.get('engine')=='rvc':
        for key in ['rvc_model','rvc_index']:
            old=config.get(key)
            if not isinstance(old,str) or Path(old).exists():continue
            # Native paths in this installed config share the current platform.
            candidate=file.parent/Path(old).name
            expected=config.get(key+'_sha256')
            if candidate.is_symlink() or not candidate.is_file() or not isinstance(expected,str):continue
            h=hashlib.sha256()
            with candidate.open('rb') as stream:
                for block in iter(lambda:stream.read(1<<20),b''):h.update(block)
            if h.hexdigest()!=expected:raise ValueError('relocated_singing_weight_hash_mismatch')
            config[key]=str(candidate)
    return config
