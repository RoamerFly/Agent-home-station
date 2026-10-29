"""Install pinned public RVC pairs; retain the original Manbo as smn-曼波.

Download on either platform, then install using the runtime's native paths.
Checkpoints are verified as data, never imported/executed by the installer.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import time
import urllib.parse
import urllib.request

from voice_library import atomic_json, digest

REPO = 'chaye741/RVC-Voice-Models'
REVISION = 'acfd9f836ca1fc862a594bc303cfccee8d565e94'
VOICES = [
    ('曼波', '卡通-曼波', 'manbo-chaye741', 'voice-manbo', [
        (55222703, 'f3707958b534264b00f8867e0f91bce5f4dbfd02c42267bea9e0c57affee6c62'),
        (141362899, '3b8772d0d21efe8b27a99d093014e21b874479d725b06f3b987ab1fb1aac7a5f')]),
    ('丁真', '丁真', 'dingzhen-chaye741', 'voice-dingzhen', [
        (55027130, '434d7468264b1641180724152003426b34d1fa418392095c07360702577c9165'),
        (31588619, '4fb5b11ea777cfc732ec7ad28354b4a2cfa4c01461d10ece4bf2829dc8a9117b')]),
    ('卢本伟', '卢本伟', 'lubenwei-chaye741', 'voice-lubenwei', [
        (55222703, '4e9e65236336de589b116320123a695cb224ae1bf29d1b41332098661d390e87'),
        (37739347, '9576023ac4bf294823f74728fda290eee225f1feb800f202d4676bc2d8232e0d')]),
]


def checked(path, size, sha):
    if path.is_symlink() or not path.is_file() or path.stat().st_size != size or digest(path) != sha:
        raise ValueError('voice_asset_integrity_failed: ' + str(path))


def fetch_asset(url, target, size, sha):
    target.parent.mkdir(parents=True, exist_ok=True)
    if target.exists():
        checked(target, size, sha)
        return
    block = 4 << 20
    ranges = [(s, min(size - 1, s + block - 1)) for s in range(0, size, block)]
    def part(bounds):
        start, end = bounds
        path = target.with_name(target.name + '.part-' + str(start))
        if path.is_symlink():
            raise ValueError('download_part_symlink')
        if path.is_file() and path.stat().st_size == end - start + 1:
            return path
        for attempt in range(4):
            try:
                request = urllib.request.Request(url + '?download=true&part=' + str(start) + '&attempt=' + str(attempt),
                    headers={'Range': f'bytes={start}-{end}', 'User-Agent': 'wechat-agent-voice-library'})
                with urllib.request.urlopen(request, timeout=45) as response:
                    if response.status != 206 or response.headers.get('Content-Range') != f'bytes {start}-{end}/{size}':
                        raise ValueError('voice_download_range_mismatch')
                    data = response.read(end - start + 2)
                if len(data) != end - start + 1:
                    raise ValueError('voice_download_incomplete')
                path.write_bytes(data)
                return path
            except (OSError, ValueError):
                if attempt == 3:
                    raise
                time.sleep(1 + attempt)
    with ThreadPoolExecutor(max_workers=4) as pool:
        parts = list(pool.map(part, ranges))
    pending = target.with_suffix(target.suffix + '.pending')
    if pending.is_symlink():
        raise ValueError('download_pending_symlink')
    with pending.open('wb') as out:
        for path in parts:
            with path.open('rb') as inp:
                while data := inp.read(1 << 20):
                    out.write(data)
    checked(pending, size, sha)
    os.replace(pending, target)
    for path in parts:
        path.unlink()


def download(workspace, roles=None):
    cache = workspace / 'public_workspace/models/voice-library/.downloads'
    for role, source_name, _, _, assets in VOICES:
        if roles and role not in roles:
            continue
        for (folder, ext), (size, sha) in zip([('weights', '.pth'), ('indices', '.index')], assets):
            installed = cache.parent / role / 'weights' / (source_name + ext)
            if installed.exists():
                checked(installed, size, sha)
                print('Verified installed:', role, ext, size, flush=True)
                continue
            remote = folder + '/' + source_name + ext
            url = f'https://huggingface.co/{REPO}/resolve/{REVISION}/' + urllib.parse.quote(remote)
            fetch_asset(url, cache / (sha + ext), size, sha)
            print('Verified:', role, ext, size, flush=True)


def retain_original(root, manifests, catalog):
    old = next((x for x in catalog['items'] if x['id'] == 'manbo-tlme'), None)
    if not old:
        return
    source, target = root / '曼波', root / 'smn-曼波'
    if old.get('directory') != 'smn-曼波':
        if source.is_symlink() or target.exists() or not source.is_dir():
            raise ValueError('original_manbo_directory_conflict')
        source.rename(target)
    old.update(name='smn-曼波 · 待兼诗歌剧', role='smn-曼波', directory='smn-曼波', profile_id='voice-smn-manbo')
    for category in ['weights', 'references', 'previews']:
        for f in old.get(category, []):
            rel = Path(f.get('relative_path', 'weights/' + f['filename']))
            if rel.parts[0] in ['曼波', 'smn-曼波']:
                rel = Path(*rel.parts[1:])
            path = target / rel
            checked(path, f['bytes'], f['sha256'])
            f.update(path=str(path), relative_path=path.relative_to(root).as_posix())
    atomic_json(target / 'role.json', {'role': 'smn-曼波', 'variants': [
        {k: old[k] for k in ['id', 'name', 'engine']}]})
    atomic_json(manifests / 'voice-smn-manbo.json', {
        'id': 'voice-smn-manbo', 'name': old['name'], 'root': str(target),
        'readme': str(target / 'README.md'), 'configuration': str(target / 'weights/singing-voice.config'),
        'usage': '保留的原版待兼诗歌剧RVC。需有人声音频，不是文本转语音；与新版卡通-曼波分别选择。'})


def install(workspace, roles=None):
    public = workspace / 'public_workspace'
    root, manifests = public / 'models/voice-library', public / 'manifests'
    root.mkdir(parents=True, exist_ok=True)
    manifests.mkdir(parents=True, exist_ok=True)
    catalog_file = root / 'catalog.json'
    catalog = json.loads(catalog_file.read_text(encoding='utf-8')) if catalog_file.exists() else {'version': 1, 'items': []}
    # Verify all inputs before altering the current catalogue or original role.
    selected = [v for v in VOICES if not roles or v[0] in roles]
    for role, source_name, _, _, assets in selected:
        for ext, (size, sha) in zip(['.pth', '.index'], assets):
            cached = root / '.downloads' / (sha + ext)
            if cached.exists():
                checked(cached, size, sha)
            else:
                checked(root / role / 'weights' / (source_name + ext), size, sha)
    if any(v[0] == '曼波' for v in selected):
        retain_original(root, manifests, catalog)
        # Publish the retained role immediately; interrupted installation can
        # be resumed without leaving its file IDs pointing at the old folder.
        atomic_json(catalog_file, catalog)
    for role, source_name, identity, profile, assets in selected:
        folder = root / role
        weights = folder / 'weights'
        weights.mkdir(parents=True, exist_ok=True)
        entries = []
        for ext, (size, sha) in zip(['.pth', '.index'], assets):
            target, cached = weights / (source_name + ext), root / '.downloads' / (sha + ext)
            if target.exists():
                checked(target, size, sha)
            else:
                os.replace(cached, target)
            entries.append({'file': sha + ext, 'filename': target.name, 'path': str(target),
                'relative_path': target.relative_to(root).as_posix(), 'sha256': sha, 'bytes': size})
        config = {'enabled': True, 'profile': identity, 'engine': 'rvc',
            'runtime': str(public / 'models/separator'),
            'python': str(public / 'runtimes/linux-music-env/bin/python3.10'),
            'rvc_python': str(public / 'runtimes/linux-music-env/bin/python3.10'),
            'rvc_project': str(public / 'projects/rvc-upstream'),
            'rvc_model': entries[0]['path'], 'rvc_index': entries[1]['path'],
            'rvc_model_sha256': entries[0]['sha256'], 'rvc_index_sha256': entries[1]['sha256'],
            'index_rate': .65, 'semitones': 0, 'rvc_device': 'cuda:0'}
        atomic_json(weights / 'singing-voice.config', config)
        item = {'id': identity, 'profile_id': profile, 'name': source_name, 'role': role, 'directory': role,
            'engine': 'RVC', 'status': '下载校验通过，待推理试听', 'weights': entries, 'references': [], 'previews': [],
            'source': 'https://huggingface.co/' + REPO, 'revision': REVISION,
            'notes': '有人声音频换声；不是独立文本转语音。型号及效果以实际推理和试听为准。'}
        catalog['items'] = [x for x in catalog['items'] if x['id'] != identity] + [item]
        atomic_json(folder / 'role.json', {'role': role, 'variants': [{k: item[k] for k in ['id', 'name', 'engine']}]})
        (folder / 'README.md').write_text(f'{source_name} RVC\n来源：{item["source"]}\n修订：{REVISION}\n'
            f'调用：voice_profile_id={profile}；先读取project_read(id=rvc)。\n需要人声音频，不可直接用文本生成该角色语音。\n', encoding='utf-8')
        atomic_json(manifests / (profile + '.json'), {'id': profile, 'name': source_name + ' RVC音色',
            'root': str(folder), 'readme': str(folder / 'README.md'),
            'configuration': str(weights / 'singing-voice.config'),
            'usage': '公开RVC音色，需输入有人声的音频；不是GPT-SoVITS或TTS。详见rvc项目及角色README。'})
    atomic_json(catalog_file, catalog)
    return [x for x in catalog['items'] if x['id'] in {v[2] for v in selected}]


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--workspace', type=Path, required=True)
    parser.add_argument('--download-only', action='store_true')
    parser.add_argument('--install-only', action='store_true')
    args = parser.parse_args()
    if not args.install_only:
        download(args.workspace)
    if not args.download_only:
        print(json.dumps({'installed': [x['name'] for x in install(args.workspace)]}, ensure_ascii=False))
