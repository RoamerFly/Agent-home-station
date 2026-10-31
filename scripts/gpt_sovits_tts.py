"""File-oriented GPT-SoVITS inference; uses explicit voice weights, never defaults.

Run with the managed interpreter. The OS executor supplies account/conversation
mounts; this adapter neither opens control credentials nor sends WeChat messages.
"""
import argparse
from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
import shutil
import uuid


@contextmanager
def inference_temporary_directory(parent):
    if os.name != 'nt':
        with tempfile.TemporaryDirectory(dir=parent, prefix='.gsv-') as directory:
            yield directory
        return
    # Python 3.13's Windows mkdir(0700) replaces inherited ACLs with an owner
    # ACL; an LPAC token cannot install it. Inherit the already private mount.
    directory = Path(parent) / ('.gsv-' + uuid.uuid4().hex)
    directory.mkdir(mode=0o755)
    try:
        yield str(directory)
    finally:
        shutil.rmtree(directory)


def install_windows_audio_loader(module):
    """Decode registered reference files when the optional TorchCodec is absent.

    Keep the upstream resampling/device operations; soundfile returns normalized
    float samples with explicit channels. No runtime download or CPU inference.
    """
    original = module.torchaudio.load
    def load(path, frame_offset=0, num_frames=-1, normalize=True, channels_first=True, format=None, buffer_size=4096, backend=None):
        import torch
        try:
            return original(path, frame_offset=frame_offset, num_frames=num_frames, normalize=normalize,
                            channels_first=channels_first, format=format, buffer_size=buffer_size, backend=backend)
        except ImportError as error:
            if 'TorchCodec' not in str(error):
                raise
        if not normalize:
            raise ValueError('soundfile_reference_decoder_requires_normalized_samples')
        import soundfile as sf
        samples, source_sr = sf.read(path, start=frame_offset, frames=num_frames, dtype='float32', always_2d=True)
        return torch.from_numpy((samples.T if channels_first else samples).copy()), source_sr
    module.torchaudio.load = load


def parser():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--project', required=True, type=Path)
    p.add_argument('--models', required=True, type=Path)
    p.add_argument('--packages', type=Path, help='registered separate dependency overlay for this model family')
    p.add_argument('--gpt', required=True, type=Path)
    p.add_argument('--sovits', required=True, type=Path)
    p.add_argument('--reference', required=True, type=Path)
    prompt = p.add_mutually_exclusive_group(required=True)
    prompt.add_argument('--prompt-text')
    prompt.add_argument('--prompt-text-file', type=Path)
    p.add_argument('--text', required=True)
    p.add_argument('--output', required=True, type=Path)
    p.add_argument('--device', default='cuda')
    p.add_argument('--version', default='v2', choices=['v1','v2','v2Pro','v2ProPlus','v3','v4','v5dev','v5turbo'])
    p.add_argument('--half', action=argparse.BooleanOptionalAction, default=True)
    p.add_argument('--text-lang', default='all_zh')
    p.add_argument('--prompt-lang', default='all_zh')
    p.add_argument('--parameters', type=Path, help='JSON object forwarded to TTS.run; explicit fields override defaults')
    p.add_argument('--overwrite', action='store_true')
    return p


def prepare(args):
    # Resolve before changing cwd; reject missing models before upstream can
    # silently replace the requested voice with its pretrained default.
    for field in ['project','models','packages','gpt','sovits','reference','output','parameters','prompt_text_file']:
        value = getattr(args, field)
        if value is not None:
            setattr(args, field, value.expanduser().resolve())
    for path in [args.project/'GPT_SoVITS/TTS_infer_pack/TTS.py', args.gpt, args.sovits, args.reference]:
        if not path.is_file():
            raise ValueError('required_inference_file_missing: ' + str(path))
    for name in ['chinese-roberta-wwm-ext-large','chinese-hubert-base']:
        if not (args.models/name/'config.json').is_file():
            raise ValueError('base_model_missing: ' + name)
    if args.packages is not None and not args.packages.is_dir():
        raise ValueError('model_package_overlay_missing')
    if args.output.exists() and not args.overwrite:
        raise FileExistsError(args.output)
    prompt = args.prompt_text if args.prompt_text is not None else args.prompt_text_file.read_text(encoding='utf-8-sig').strip()
    request = {'text': args.text, 'text_lang': args.text_lang,
               'ref_audio_path': str(args.reference), 'prompt_text': prompt,
               'prompt_lang': args.prompt_lang, 'text_split_method': 'cut5',
               'seed': -1, 'streaming_mode': False, 'return_fragment': False}
    if args.parameters:
        extra = json.loads(args.parameters.read_text(encoding='utf-8-sig'))
        if not isinstance(extra, dict):
            raise ValueError('parameters_must_be_object')
        request.update(extra)
    # Streaming yields may overlap/replace previous audio. This entrypoint
    # produces a complete WAV; use upstream api_v2.py for streaming output.
    if request.get('streaming_mode') or request.get('return_fragment') or request.get('gpt_streaming'):
        raise ValueError('streaming_requires_upstream_api_v2; file_entry_accepts_full_audio_only')
    if not str(request.get('text', '')).strip():
        raise ValueError('empty_synthesis_text')
    if not Path(request['ref_audio_path']).is_file():
        raise ValueError('reference_audio_missing')
    return request


def synthesize(args):
    request = prepare(args)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    # Caches and config writes belong to the private result directory, not the
    # shared source tree (ordinary callers mount public source read-only).
    cache = args.output.parent/'.gsv-cache'
    cache.mkdir(exist_ok=True)
    os.environ.setdefault('HF_HOME', str(cache/'huggingface'))
    os.environ.setdefault('HF_HUB_OFFLINE','1')
    os.environ.setdefault('TRANSFORMERS_OFFLINE','1')
    os.environ.setdefault('NUMBA_CACHE_DIR', str(cache/'numba'))
    os.environ.setdefault('MPLCONFIGDIR', str(cache/'matplotlib'))
    os.environ['GPT_SOVITS_TEXT_CACHE'] = str(cache)
    os.environ['GPT_SOVITS_G2PW_MODEL'] = str(args.models/'G2PWModel')
    os.environ['GPT_SOVITS_LANG_MODEL'] = str(args.models/'fast_langdetect')
    os.environ['bert_path'] = str(args.models/'chinese-roberta-wwm-ext-large')
    os.environ.setdefault('G2PW', '0')
    os.environ['version'] = args.version
    os.chdir(args.project)
    sys.path[:0] = [str(args.project), str(args.project/'GPT_SoVITS')]
    if args.packages is not None:
        # Keep GPT-SoVITS' Transformers stack separate from YuE's stack.
        sys.path.insert(0, str(args.packages))
    import torch
    if args.device.startswith('cuda') and not torch.cuda.is_available():
        raise RuntimeError('cuda_unavailable: request server_exec/server_process gpu=true and the registered CUDA interpreter')
    import importlib
    inference_module = importlib.import_module('TTS_infer_pack.TTS')
    if os.name == 'nt':
        install_windows_audio_loader(inference_module)
    TTS, TTS_Config = inference_module.TTS, inference_module.TTS_Config
    import numpy as np
    import soundfile as sf
    config = TTS_Config({'custom': {'version':args.version, 'device':args.device,
        'is_half':args.half and args.device != 'cpu',
        't2s_weights_path':str(args.gpt),'vits_weights_path':str(args.sovits),
        'bert_base_path':str(args.models/'chinese-roberta-wwm-ext-large'),
        'cnhuhbert_base_path':str(args.models/'chinese-hubert-base')}})
    with inference_temporary_directory(args.output.parent) as tmp:
        config.configs_path = str(Path(tmp)/'tts.yaml')
        model = TTS(config)
        chunks, rate = [], None
        for sr, audio in model.run(request):
            if rate is not None and rate != sr:
                raise ValueError('inconsistent_output_sample_rate')
            rate = sr
            chunks.append(audio)
        if not chunks or sum(len(x) for x in chunks) == 0:
            raise ValueError('empty_synthesis_output')
        audio = np.concatenate(chunks)
        output = Path(tmp)/'result.wav'
        sf.write(output, audio, rate, subtype='PCM_16')
        info = sf.info(output)
        if info.frames == 0:
            raise ValueError('empty_written_audio')
        sha = hashlib.sha256(output.read_bytes()).hexdigest()
        # Hard-link publication refuses a concurrent overwrite unless requested.
        if args.overwrite:
            os.replace(output, args.output)
        else:
            os.link(output, args.output)
    return {'output':str(args.output),'sample_rate':rate,'seconds':info.duration,
            'sha256':sha,'device':args.device,'version':model.configs.version,
            'gpt':str(args.gpt),'sovits':str(args.sovits),'effective_parameters':request,
            'wechat_sends':0}


if __name__ == '__main__':
    print(json.dumps(synthesize(parser().parse_args()), ensure_ascii=False))
