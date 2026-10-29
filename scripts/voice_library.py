"""Collect existing voices without executing checkpoints or changing defaults.

Personal models/references stay in the administrator's private library. Public
Manbo models have their own shared managed directory. No API calls or training.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import tempfile
import urllib.parse
import urllib.request


MANBO_REPO = 'TLME/RVC-Umamusume'
MANBO_REVISION = '49cc012b5ad11274a33d61affb11723ec854f944'
MANBO_FOLDER = 'Models/待兼诗歌剧(マチカネタンホイザ)'
MANBO_FILES = [
    ('uma-Matikane_Tannhauser.pth', 57589983, '305ca8238d2d8cecdcf19812f4c85c1c622e8a190af0199ee05d2b1b83d94cd2'),
    ('added_IVF1310_Flat_nprobe_1_uma-Matikane_Tannhauser_v2.index', 161487619, '93907fce2fcb38a218a5dc66feb45720c9d4d09300bc31f6e035ab7389fc0bae'),
]


def digest(path):
    h = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1 << 20), b''):
            h.update(chunk)
    return h.hexdigest()


def atomic_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(dir=path.parent, prefix='.pending-')
    try:
        with os.fdopen(fd, 'w', encoding='utf-8') as stream:
            json.dump(value, stream, ensure_ascii=False, indent=2)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(name, path)
    finally:
        Path(name).unlink(missing_ok=True)


def store_file(source, destination):
    if source.is_symlink() or not source.is_file():
        raise ValueError('voice_source_not_regular')
    before = source.stat()
    sha = digest(source)
    name = sha + source.suffix.lower()
    target = destination / name
    destination.mkdir(parents=True, exist_ok=True)
    if target.exists():
        if target.is_symlink() or digest(target) != sha:
            raise ValueError('existing_voice_object_corrupt')
    else:
        fd, pending = tempfile.mkstemp(dir=destination, prefix='.pending-')
        try:
            with os.fdopen(fd, 'wb') as out, source.open('rb') as inp:
                shutil.copyfileobj(inp, out, 1 << 20)
                out.flush()
                os.fsync(out.fileno())
            if digest(Path(pending)) != sha or source.stat().st_mtime_ns != before.st_mtime_ns:
                raise ValueError('voice_source_changed_during_import')
            # Publish without replacing any concurrent import.
            try:
                os.link(pending, target)
            except FileExistsError:
                if digest(target) != sha:
                    raise ValueError('concurrent_voice_object_mismatch')
        finally:
            Path(pending).unlink(missing_ok=True)
    return {'file': name, 'filename': source.name, 'sha256': sha, 'bytes': before.st_size}


def import_history(history, library):
    groups = {}
    def group(name):
        return groups.setdefault(name, {'id': 'gsv-' + hashlib.sha256(name.encode()).hexdigest()[:16],
            'name': name, 'engine': 'GPT-SoVITS', 'status': '已归档，待运行验证',
            'notes': 'GPT与SoVITS分别保留全部训练轮次；推荐组合只按轮次排序，不代表听感最好。',
            'weights': [], 'references': [], 'previews': [], 'source': str(history)})
    for folder, kind in [('GPT_weights_v2', 'gpt'), ('SoVITS_weights_v2', 'sovits'), ('SoVITS_weights', 'sovits-v1')]:
        if not (history / folder).is_dir():
            continue
        for file in sorted((history / folder).iterdir()):
            pattern = r'(.+)-e(\d+)\.ckpt' if kind == 'gpt' else r'(.+)_e(\d+)_s\d+\.pth'
            match = re.fullmatch(pattern, file.name)
            if not match:
                continue
            name, epoch = match.groups()
            info = store_file(file, library / 'files')
            info.update(kind=kind, epoch=int(epoch), version='v1' if kind.endswith('v1') else 'v2')
            group(name)['weights'].append(info)
    # Only explicit reference/example pairs. Never sweep arbitrary user audio.
    for stem, name in [('Chikusyoudou', 'Chikusyoudou'), ('konan', 'konan'), ('lfy_chinese', 'lfy-chinese'), ('LiuFeiyang', 'LiuFeiyang2'), ('mom', 'mom')]:
        for suffix in ['.wav', '.txt']:
            file = history / 'A_Example' / (stem + '_example' + suffix)
            if file.is_file() and name in groups:
                groups[name]['references'].append(store_file(file, library / 'files'))
    for name, item in groups.items():
        item['weights'].sort(key=lambda x: (x['kind'], x['epoch']))
        item['recommended_weights'] = {}
        for kind in ['gpt', 'sovits', 'sovits-v1']:
            choices = [x for x in item['weights'] if x['kind'] == kind]
            if choices:
                item['recommended_weights'][kind] = choices[-1]['file']
        item['paired'] = 'gpt' in item['recommended_weights'] and 'sovits' in item['recommended_weights']
        if not item['paired']:
            item['status'] = '权重不齐，待补配'
        if name == 'LiuFeiyang2':
            item['notes'] += ' 参考录音来自LiuFeiyang_example，文件名关联待试听确认。'
    return list(groups.values())


def import_whale(workspace, library):
    items = []
    for family, design, directory, key in [
        ('design', 'bluewhale-candidates.json', 'voice-design-bailian', 'candidates'),
        ('clone', 'bluewhale-reference-variants.json', 'voice-clone-bailian', 'variants'),
    ]:
        config = json.loads((workspace / 'design/voice' / design).read_text(encoding='utf-8'))
        for candidate in config[key]:
            code, name = candidate['code'], candidate['name']
            preview = workspace / 'research/evidence' / directory / f'试听-{code}-{name}.mp3'
            if not preview.is_file():
                continue
            item = {'id': f'whale-{family}-{code.lower()}', 'name': f'大肥鱼 · {code} {name}',
                    'engine': '百炼 TTS', 'status': '当前已选' if family == 'clone' and code == 'C' else '试听候选',
                    'notes': '表达版本不等于独立训练模型。百炼音色登记仍在secrets，密钥不复制。',
                    'weights': [], 'references': [], 'previews': [store_file(preview, library / 'files')],
                    'private_registration': 'secrets/bluewhale-voice-' + ('clone' if family == 'clone' else 'design') + '.json',
                    'variant': code, 'synthesis_instruction': candidate.get('instruction', candidate.get('voice_prompt', ''))}
            items.append(item)
    model_root = workspace / 'secrets/rvc/project/assets/weights'
    for model in sorted(model_root.glob('bluewhale*.pth')):
        items.append({'id': 'rvc-' + hashlib.sha256(model.name.encode()).hexdigest()[:16],
            'name': '大肥鱼 · ' + model.stem, 'engine': 'RVC', 'status': '试训存档，音质未通过',
            'notes': '使用C说话合成素材训练，歌声相似度仍未认可；不自动激活。',
            'weights': [dict(store_file(model, library / 'files'), kind='rvc')], 'references': [], 'previews': []})
    for file in sorted((workspace / 'research/evidence/rvc-upgrade').glob('大肥鱼*.mp3')):
        items.append({'id': 'trial-' + hashlib.sha256(file.name.encode()).hexdigest()[:16],
            'name': file.stem, 'engine': 'Seed-VC' if 'Seed' in file.name else 'RVC',
            'status': '试听未通过', 'notes': '保留历史对比样本，不冒充已选歌声。',
            'weights': [], 'references': [], 'previews': [store_file(file, library / 'files')]})
    index = workspace / 'public_workspace/models/voice-C/bluewhale-C-trial-flat.index'
    if index.is_file():
        info = dict(store_file(index, library / 'files'), kind='rvc-index')
        for item in items:
            if item['engine'] == 'RVC' and item['weights']:
                item['weights'].append(info)
    return items


def download_manbo(public):
    folder = public / 'manbo-tlme'
    folder.mkdir(parents=True, exist_ok=True)
    results = []
    for name, size, expected in MANBO_FILES:
        path = folder / name
        url = f'https://huggingface.co/{MANBO_REPO}/resolve/{MANBO_REVISION}/' + urllib.parse.quote(MANBO_FOLDER + '/' + name, safe='/')
        if not path.exists():
            fd, pending = tempfile.mkstemp(dir=folder, prefix='.download-')
            try:
                os.close(fd)
                # CDN full responses can stall before headers. Read bounded
                # independent ranges, validate each range and the final LFS hash.
                chunks = folder / ('.chunks-' + expected)
                chunks.mkdir(exist_ok=True)
                block = 4 << 20
                ranges = [(start, min(size - 1, start + block - 1)) for start in range(0, size, block)]
                def fetch(bounds):
                    start, end = bounds
                    part = chunks / str(start)
                    if part.is_file() and part.stat().st_size == end - start + 1:
                        return part
                    request = urllib.request.Request(url + '?download=true&chunk=' + str(start), headers={'Range': f'bytes={start}-{end}'})
                    with urllib.request.urlopen(request, timeout=60) as response:
                        if response.status != 206 or response.headers.get('Content-Range') != f'bytes {start}-{end}/{size}':
                            raise ValueError('model_download_range_mismatch')
                        data = response.read(end - start + 2)
                    if len(data) != end - start + 1:
                        raise ValueError('model_download_range_incomplete')
                    part.write_bytes(data)
                    return part
                with ThreadPoolExecutor(max_workers=4) as pool:
                    parts = list(pool.map(fetch, ranges))
                with Path(pending).open('wb') as out:
                    for part in parts:
                        with part.open('rb') as inp:
                            shutil.copyfileobj(inp, out, 1 << 20)
                if Path(pending).stat().st_size != size or digest(Path(pending)) != expected:
                    raise ValueError('download_size_or_hash_mismatch')
                try:
                    os.link(pending, path)
                except FileExistsError:
                    if path.is_symlink() or path.stat().st_size != size or digest(path) != expected:
                        raise ValueError('concurrent_manbo_download_mismatch')
                for part in parts:
                    part.unlink()
                chunks.rmdir()
            finally:
                Path(pending).unlink(missing_ok=True)
        if path.is_symlink() or path.stat().st_size != size or digest(path) != expected:
            raise ValueError('manbo_model_integrity_failed')
        results.append({'filename': name, 'path': str(path), 'bytes': size, 'sha256': expected, 'url': url})
        print('Verified Manbo asset:', name, size, flush=True)
    return {'id': 'manbo-tlme', 'name': '曼波 · 待兼诗歌剧', 'engine': 'RVC v2',
        'status': '下载校验通过，待推理试听', 'notes': '日语角色转换模型；需输入有人声的音频。不是独立文本转语音模型。未自动加载第三方pickle权重。',
        'weights': results, 'references': [], 'previews': [], 'source': 'https://huggingface.co/' + MANBO_REPO,
        'revision': MANBO_REVISION, 'license': '模型仓库标注MIT；角色/原始声音权利另属权利人，未据此推定商业授权。'}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--workspace', type=Path, required=True)
    parser.add_argument('--history', type=Path, required=True)
    parser.add_argument('--library', type=Path, required=True)
    parser.add_argument('--download-manbo', action='store_true')
    args = parser.parse_args()
    library = args.library.resolve()
    existing = json.loads((library / 'catalog.json').read_text(encoding='utf-8')) if (library / 'catalog.json').exists() else {'items': []}
    items = import_history(args.history.resolve(strict=True), library)
    items += import_whale(args.workspace.resolve(strict=True), library)
    collected = {item['id']: item for item in existing['items']}
    collected.update({item['id']: item for item in items})
    def publish():
        atomic_json(library / 'catalog.json', {'version': 1, 'items': list(collected.values()),
            'policy': '本机管理员音色库；个人权重及参考不挂载给普通群友。仅归档，不修改默认音色，不训练、不发送微信。'})
    publish()  # Remote failure must not hide successfully imported local voices.
    if args.download_manbo:
        item = download_manbo(args.workspace / 'public_workspace/models/voice-library')
        collected[item['id']] = item
        publish()
    print(json.dumps({'voices': len(collected), 'library': str(library)}, ensure_ascii=False))


if __name__ == '__main__':
    main()
