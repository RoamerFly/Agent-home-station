"""Retrieve selected members from a disclosed modified public IPA for static study.

Never executes or installs the package. HTTP ranges avoid downloading themes and
plugins. All redirects must be supplied by the public feed's existing URL; no
signature is changed or inferred. Content identity is recorded after retrieval.
"""
import datetime
import hashlib
import io
import json
import pathlib
import plistlib
import re
import urllib.parse
import urllib.request
import zipfile

ROOT = pathlib.Path(__file__).resolve().parents[2]
OUT = ROOT / 'research/evidence/public-ipa-8078'
OUT.mkdir(exist_ok=True)
source = json.loads((ROOT / 'research/evidence/alternative-ipa-sources.json').read_text(encoding='utf-8-sig'))['qualified_candidate']
url = source['public_download_url']
parts = urllib.parse.urlsplit(url)
url = urllib.parse.urlunsplit((parts.scheme, parts.netloc, urllib.parse.quote(parts.path), parts.query, parts.fragment))


class RemoteZIP(io.RawIOBase):
    def __init__(self):
        self.position = 0
        self.size = source['redirect_advertised_size_bytes']
        self.requests = []

    def seekable(self): return True
    def readable(self): return True
    def tell(self): return self.position

    def seek(self, offset, whence=0):
        self.position = offset if whence == 0 else self.position + offset if whence == 1 else self.size + offset
        if not 0 <= self.position <= self.size: raise ValueError('out-of-object seek')
        return self.position

    def read(self, length=-1):
        if length < 0: length = self.size - self.position
        length = min(length, self.size - self.position)
        if not length: return b''
        if length > 600 << 20: raise ValueError('per-read bound')
        start = self.position
        req = urllib.request.Request(url, headers={'Range': f'bytes={start}-{start + length - 1}', 'User-Agent': 'Mozilla/5.0'})
        with urllib.request.urlopen(req, timeout=90) as r:
            expected = f'bytes {start}-{start + length - 1}/{self.size}'
            if r.status != 206 or r.headers.get('Content-Range') != expected:
                raise ValueError(f'unsupported public range response: {r.status}')
            chunks = []
            remaining = length
            while remaining:
                b = r.read(min(1 << 20, remaining))
                if not b: raise ValueError('truncated range')
                chunks.append(b)
                remaining -= len(b)
                if length > 16 << 20 and (length - remaining) // (32 << 20) != (length - remaining - len(b)) // (32 << 20):
                    print('Static member range downloaded MiB:', (length - remaining) // (1 << 20), flush=True)
        self.position += length
        self.requests.append({'offset': start, 'length': length})
        return b''.join(chunks)


remote = RemoteZIP()
with zipfile.ZipFile(remote) as archive:
    info_names = [i.filename for i in archive.infolist() if re.fullmatch(r'Payload/[^/]+\.app/Info\.plist', i.filename)]
    assert len(info_names) == 1
    info_raw = archive.read(info_names[0])
    info = plistlib.loads(info_raw)
    binary_name = info_names[0].rsplit('/', 1)[0] + '/' + info['CFBundleExecutable']
    member = archive.getinfo(binary_name)
    report = {'source_feed': source['feed_url'], 'public_download_url': source['public_download_url'],
              'retrieved_at_utc': datetime.datetime.now(datetime.timezone.utc).isoformat(),
              'scope': 'static reading only; disclosed third-party themes/plugins; not an official/clean package',
              'executed': False, 'installed': False, 'whole_ipa_downloaded': False,
              'object_bytes': remote.size,
              'metadata': {k: info.get(k) for k in ['CFBundleIdentifier', 'CFBundleExecutable', 'CFBundleShortVersionString', 'CFBundleVersion', 'MinimumOSVersion', 'DTSDKName']},
              'main_binary': {'member': binary_name, 'bytes': member.file_size, 'compressed_bytes': member.compress_size},
              'injected_library_paths': [i.filename for i in archive.infolist() if i.filename.endswith('.dylib')][:100]}
    (OUT / 'Info.plist').write_bytes(info_raw)
    print(json.dumps({'metadata': report['metadata'], 'main_binary': report['main_binary']}, ensure_ascii=True), flush=True)
    assert member.file_size < 1536 << 20 and member.compress_size < 600 << 20
    target = OUT / 'WeChat.macho.bin'
    if not target.exists():
        data = archive.read(binary_name)  # zipfile checks the member CRC.
        target.write_bytes(data)
    report['main_binary']['sha256'] = hashlib.sha256(target.read_bytes()).hexdigest()
    report['range_reads'] = remote.requests
    (OUT / 'inventory.json').write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding='utf-8')
    print(json.dumps({'saved': str(target), 'sha256': report['main_binary']['sha256'], 'executed': False}), flush=True)
