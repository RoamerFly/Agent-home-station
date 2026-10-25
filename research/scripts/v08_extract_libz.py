"""Extract only the regular libz target from frozen, digest-verified OCI layers.

Tar member paths are inspected as strings; extractall is never used. No code is
executed. The resulting binary remains research evidence, not an app dependency.
"""
import base64
import hashlib
import json
from pathlib import Path
import tarfile

ROOT = Path(__file__).resolve().parents[2]
CACHE = Path('C:/Temp/wechat-bot-research-20261023/wechat-ipad-static')
TARGET = 'usr/wic-go/lib/libz.so.1.3.1'


def digest(data):
    return 'sha256:' + hashlib.sha256(data).hexdigest()


def main():
    wrapper = json.loads((ROOT / 'research/evidence/manifest.json').read_text(encoding='utf-8-sig'))
    raw = base64.b64decode(wrapper['Raw'])
    assert digest(raw) == wrapper['Descriptor']['digest']
    manifest = json.loads(raw)
    value = None
    last_layer = None
    aliases = []
    for index, layer in enumerate(manifest['layers']):
        path = CACHE / f'layer-{index}.tar.gz'
        assert digest(path.read_bytes()) == layer['digest']
        with tarfile.open(path, 'r:gz') as archive:
            for member in archive:
                name = member.name.removeprefix('./').lstrip('/')
                if '/.wh.' in name or name.startswith('.wh.'):
                    raise RuntimeError('Manual whiteout review required: ' + name)
                if name == 'usr/wic-go/lib/libz.so':
                    alias_digest = None
                    if member.isfile():
                        if member.size > 8 << 20:
                            raise RuntimeError('Invalid libz alias size')
                        alias_digest = digest(archive.extractfile(member).read())
                    aliases.append(dict(layer=index, name=name, symlink=member.issym(), linkname=member.linkname,
                                        regular=member.isfile(), bytes=member.size, sha256=alias_digest))
                if name != TARGET:
                    continue
                if not member.isfile() or member.size > 8 << 20:
                    raise RuntimeError('Invalid libz member')
                value = archive.extractfile(member).read()
                last_layer = index
    assert value is not None
    assert aliases and aliases[-1]['regular'] and aliases[-1]['sha256'] == digest(value), 'Loaded libz.so differs from versioned file'
    output = ROOT / 'research/evidence/v08-libz.so.1.3.1.bin'
    if output.exists():
        assert output.read_bytes() == value
    else:
        output.write_bytes(value)
    record = dict(authority='Last regular-file write across digest-verified frozen OCI layers; static only',
                  manifest_digest=digest(raw), target=TARGET, last_layer=last_layer,
                  bytes=len(value), sha256=digest(value), aliases=aliases, executed=False)
    (ROOT / 'research/evidence/v08-libz-extraction.json').write_text(json.dumps(record, indent=2) + '\n', encoding='utf-8')
    print(json.dumps(record, indent=2))


if __name__ == '__main__':
    main()
