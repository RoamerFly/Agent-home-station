"""Register/copy a local GPT-SoVITS checkout into a managed public workspace.

Only committed source and explicitly named common base models are copied.
User-trained checkpoints, training data, logs and source venvs are not imported.
Dependency installation is a separate, explicit step in the usage document.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import subprocess
import tarfile
import tempfile


PLUGIN_ENTRY = '''"""Diagnostic entry for a managed GPT-SoVITS resource project.

Inference belongs to the installed speech plugin. This resource entry never
loads models, forwards arguments or locates application source code.
"""
import json
import sys

if sys.argv[1:] in (["--help"], ["-h"]):
    print("Managed GPT-SoVITS resources only. Use speech_synthesize with an "
          "installed, enabled and configured speech plugin and an explicitly "
          "selected voice_id. This entry does not run inference. Direct project "
          "development requires separate explicit authorization.")
else:
    print(json.dumps({"error": "speech_plugin_required", "inference_owner": "plugin:speech", "wechat_sends": 0}))
    raise SystemExit(2)
'''


def install_plugin_entry(project):
    """Replace only the managed diagnostic entry; no model or SDK copy."""
    project = Path(project)
    entry = project / 'gpt_sovits_tts.py'
    for path in [entry, project, *project.parents]:
        if path.is_symlink() or getattr(path, 'is_junction', lambda: False)():
            raise ValueError('linked_install_target_not_allowed')
    if not project.is_dir():
        raise ValueError('gpt_sovits_registered_project_required')
    data = PLUGIN_ENTRY.encode('utf8')
    if entry.exists() and not entry.is_file():
        raise ValueError('gpt_sovits_entry_not_regular')
    if entry.exists() and entry.read_bytes() == data:
        return entry
    with tempfile.NamedTemporaryFile(dir=project, delete=False) as output:
        pending = Path(output.name)
        output.write(data)
    try:
        os.replace(pending, entry)
    finally:
        pending.unlink(missing_ok=True)
    return entry


def mark_plugin_inference(manifest, entry):
    """Keep resource registration and model ownership separate."""
    manifest.update({
        'entry': str(entry),
        'inference_owner': 'plugin:speech',
        'runtime_status': 'registered_requires_installed_configured_speech_plugin',
        'execution': 'speech_synthesize via installed, enabled and configured speech plugin; registered entry is diagnostic only',
        'usage': '已登记的公共 GPT-SoVITS 源码和基础模型可供显式授权的项目开发使用。命名语音生成使用已安装、启用并配置 local_profile_id=gpt-sovits 的 speech 插件，通过 speech_synthesize 显式选择 voice_id。entry 仅诊断，不能推理，也不是宿主信任执行来源；宿主只执行已验证的安装包 runner。手动项目开发需独立显式授权。不会自动发送微信。',
    })
    return manifest


def source_member_allowed(name):
    parts = PurePosixPath(name).parts
    if any(part.startswith(('GPT_weights', 'SoVITS_weights', '.venv')) or part in
           {'logs','TEMP','output','outputs','runtime','pretrained_models','__pycache__'} for part in parts):
        return False
    if name in {'weight.json','GPT_SoVITS/configs/tts_infer.yaml'}:
        return False
    return PurePosixPath(name).suffix.lower() not in {'.ckpt','.pth','.safetensors','.onnx','.bin','.wav','.mp3','.mp4','.flac'}


def sha(path):
    h = hashlib.sha256()
    with path.open('rb') as f:
        for block in iter(lambda: f.read(1 << 20), b''):
            h.update(block)
    return h.hexdigest()


def copy_verified(source, target):
    if source.is_symlink() or getattr(source, 'is_junction', lambda: False)():
        raise ValueError('linked_import_not_allowed')
    if source.is_dir():
        if target.is_symlink() or getattr(target, 'is_junction', lambda: False)():
            raise ValueError('linked_target_not_allowed')
        target.mkdir(parents=True, exist_ok=True)
        for item in source.iterdir():
            if item.name not in ['__pycache__', 'record.log']:
                copy_verified(item, target/item.name)
        return
    if not source.is_file():
        raise ValueError('import_source_not_regular')
    expected = sha(source)
    if target.exists():
        if target.is_symlink() or sha(target) != expected:
            raise ValueError('existing_managed_asset_differs: '+str(target))
        return
    target.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(dir=target.parent, delete=False) as f:
        pending = Path(f.name)
    try:
        shutil.copyfile(source, pending)
        if sha(pending) != expected or sha(source) != expected:
            raise ValueError('source_changed_during_copy')
        os.link(pending, target)
    finally:
        pending.unlink(missing_ok=True)


def register(source, public, python):
    for path in [source, public, python.parent]:
        for part in [path, *path.parents]:
            if part.is_symlink() or getattr(part, 'is_junction', lambda: False)():
                raise ValueError('linked_install_path_not_allowed')
    source, public, python = source.resolve(), public.resolve(), python.absolute()
    for relative in ['projects','models','manifests','projects/gpt-sovits','models/gpt-sovits']:
        target = public/relative
        if target.is_symlink() or getattr(target, 'is_junction', lambda: False)():
            raise ValueError('linked_install_target_not_allowed')
    project = public/'projects/gpt-sovits'
    if not (source/'api_v2.py').is_file():
        raise ValueError('not_gpt_sovits_checkout')
    revision = subprocess.check_output(['git','-C',str(source),'rev-parse','HEAD'],text=True).strip()
    if not project.exists():
        project.parent.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(dir=project.parent, prefix='.gsv-install-') as tmp:
            stage = Path(tmp)/'source'
            stage.mkdir()
            archive = Path(tmp)/'source.tar'
            subprocess.run(['git','-C',str(source),'archive','-o',str(archive),'HEAD'],check=True)
            with tarfile.open(archive) as stream:
                for item in stream.getmembers():
                    if item.issym() or item.islnk() or not (stage/item.name).resolve().is_relative_to(stage):
                        raise ValueError('unsafe_source_archive')
                options = {'filter':'data'} if hasattr(tarfile, 'data_filter') else {}
                stream.extractall(stage, members=[item for item in stream.getmembers() if source_member_allowed(item.name)], **options)
            # No source user's active API config/weight selection is imported.
            config = stage/'GPT_SoVITS/configs/tts_infer.yaml'
            if config.exists():
                config.unlink()
            (stage/'SOURCE_REVISION').write_text(revision+'\n',encoding='utf-8')
            stage.rename(project)
    elif (project/'SOURCE_REVISION').read_text(encoding='utf-8').strip() != revision:
        raise ValueError('existing_project_revision_differs; stage_an_explicit_upgrade')
    common = public/'models/gpt-sovits'
    for folder in ['chinese-roberta-wwm-ext-large','chinese-hubert-base']:
        copy_verified(source/'GPT_SoVITS/pretrained_models'/folder,common/folder)
    # Keep auxiliary base models with models too. Early staged copies may
    # exist under the source tree; move only these known managed directories.
    for relative in ['GPT_SoVITS/pretrained_models/fast_langdetect','GPT_SoVITS/text/G2PWModel']:
        if (source/relative).exists():
            target = common/Path(relative).name
            old = project/relative
            if old.exists() and not target.exists():
                if old.is_symlink():
                    raise ValueError('linked_auxiliary_model')
                old.rename(target)
            copy_verified(source/relative, target)
    here = Path(__file__).resolve().parent
    install_plugin_entry(project)
    for name in ['gpt_sovits_inference_requirements.txt']:
        shutil.copyfile(here/name, project/name)
    doc = here.parent/'docs/agent/gpt-sovits-runtime.md'
    shutil.copyfile(doc, project/'AGENT_USAGE.md')
    workflow = here.parent/'docs/media/gpt-sovits-rvc-workflow.md'
    shutil.copyfile(workflow, project/'VOICE_WORKFLOW.md')
    # Upstream's portable bundle uses a Windows-only FFmpeg path. Keep the
    # compatibility delta explicit and leave the user's original untouched.
    portable = project/'tools/portable_runtime.py'
    if portable.exists():
        text = portable.read_text(encoding='utf-8')
        old = 'FFMPEG_EXE = RUNTIME_DIR / "ffmpeg.exe"'
        new = 'FFMPEG_EXE = RUNTIME_DIR / "ffmpeg.exe" if os.name == "nt" else "ffmpeg"'
        portable.write_text('\n'.join(new if line.startswith(old) else line for line in text.splitlines())+'\n',encoding='utf-8')
    for relative, names in [
        ('GPT_SoVITS/text/g2pw/g2pw.py',['polyphonic.pickle','polyphonic.md5']),
        ('GPT_SoVITS/text/english.py',['engdict_cache.pickle','namedict_cache.pickle']),
    ]:
        path = project/relative
        text = path.read_text(encoding='utf-8')
        for name in names:
            text = text.replace('os.path.join(current_file_path, "'+name+'")',
                                'os.path.join(os.environ.get("GPT_SOVITS_TEXT_CACHE", current_file_path), "'+name+'")')
        path.write_text(text,encoding='utf-8')
    chinese = project/'GPT_SoVITS/text/chinese2.py'
    text = chinese.read_text(encoding='utf-8').replace('model_dir="GPT_SoVITS/text/G2PWModel"',
        'model_dir=os.environ.get("GPT_SOVITS_G2PW_MODEL", "GPT_SoVITS/text/G2PWModel")')
    chinese.write_text(text,encoding='utf-8')
    detector = project/'GPT_SoVITS/text/LangSegmenter/langsegmenter.py'
    text = detector.read_text(encoding='utf-8')
    if 'import os\n' not in text:
        text = text.replace('import logging\n', 'import logging\nimport os\n')
    text = text.replace('cache_dir=Path(__file__).parent.parent.parent / "pretrained_models" / "fast_langdetect"',
        'cache_dir=Path(os.environ["GPT_SOVITS_LANG_MODEL"]) if "GPT_SOVITS_LANG_MODEL" in os.environ else Path(__file__).parent.parent.parent / "pretrained_models" / "fast_langdetect"')
    detector.write_text(text,encoding='utf-8')
    manifest = {'id':'gpt-sovits','name':'GPT-SoVITS 本地角色语音合成',
        'root':str(project),'readme':str(project/'AGENT_USAGE.md'),
        'python':str(python),'entry':str(project/'gpt_sovits_tts.py'),
        'voice_workflow':str(project/'VOICE_WORKFLOW.md'),
        'base_models':str(common),'source_revision':revision,
        'upstream':'https://github.com/RVC-Boss/GPT-SoVITS',
        'scope':'公共代码、基础模型和依赖共享；个人声线及输出留在账号私有工作区。GPT-SoVITS TTS与RVC原曲换声是不同流程。'}
    mark_plugin_inference(manifest, project/'gpt_sovits_tts.py')
    directory = public/'manifests'
    directory.mkdir(exist_ok=True)
    with tempfile.NamedTemporaryFile(dir=directory, delete=False, mode='w',encoding='utf-8') as f:
        pending = Path(f.name)
        json.dump(manifest,f,ensure_ascii=False,indent=2)
    os.replace(pending,directory/'gpt-sovits.json')
    return manifest


if __name__ == '__main__':
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--source',required=True,type=Path)
    p.add_argument('--public-root',required=True,type=Path)
    p.add_argument('--python',required=True,type=Path)
    a=p.parse_args()
    print(json.dumps(register(a.source,a.public_root,a.python),ensure_ascii=False))
