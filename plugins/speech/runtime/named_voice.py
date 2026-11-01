"""Strict named-voice bridge. Only the host can resolve private model resources."""
import importlib.util
import math
from pathlib import Path
import re

_spec = importlib.util.spec_from_file_location('named_parameter_policy', Path(__file__).with_name('gpt_sovits.py'))
_runner = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_runner)


def _object(value, keys, error):
    if not isinstance(value, dict) or set(value) != keys:
        raise ValueError(error)
    return value


def _string(value, error):
    if not isinstance(value, str) or not value.strip() or '\x00' in value:
        raise ValueError(error)
    return value


def _hash(value):
    if not isinstance(value, str) or not re.fullmatch('[a-f0-9]{64}', value):
        raise ValueError('invalid_named_voice_hash')
    return value


def synthesize(args, config, request):
    _object(args, {'voice', 'text', 'device', 'version', 'parameters', 'timeout_seconds'}, 'invalid_named_voice_arguments')
    voice = _object(args['voice'], {'id', 'version'}, 'invalid_named_voice_identity')
    if not isinstance(voice['id'], str) or not re.fullmatch('[A-Za-z0-9_.@-]{1,128}', voice['id']) or voice['id'] in ('.', '..'):
        raise ValueError('invalid_named_voice_identity')
    _hash(voice['version'])
    _string(args['text'], 'invalid_named_voice_text')
    if not isinstance(args['device'], str) or not re.fullmatch(r'(cpu|cuda(?::[0-9]+)?)', args['device']):
        raise ValueError('invalid_named_voice_device')
    if args['version'] not in ('v1', 'v2', 'v2Pro', 'v2ProPlus', 'v3', 'v4', 'v5dev', 'v5turbo'):
        raise ValueError('invalid_named_voice_model_version')
    timeout = args['timeout_seconds']
    if type(timeout) not in (int, float) or not math.isfinite(timeout) or timeout <= 0:
        raise ValueError('invalid_named_voice_timeout')
    _runner.validate_parameters(args['parameters'])
    if config.get('local_profile_id') != 'gpt-sovits':
        raise ValueError('named_voice_profile_not_configured')
    result = request(args)
    _object(result, {'status', 'audio', 'voice', 'device', 'version'}, 'invalid_named_voice_result')
    if result['status'] != 'synthesized' or result['voice'] != voice or result['device'] != args['device'] or result['version'] != args['version']:
        raise ValueError('named_voice_result_binding_mismatch')
    _object(result['voice'], {'id', 'version'}, 'invalid_named_voice_result')
    audio = _object(result['audio'], {'ref', 'sha256', 'bytes', 'sample_rate', 'seconds'}, 'invalid_named_voice_audio')
    if audio['ref'] != 'audio_0':
        raise ValueError('invalid_named_voice_audio')
    _hash(audio['sha256'])
    if any(type(audio[key]) is not int or audio[key] <= 0 for key in ('bytes', 'sample_rate')):
        raise ValueError('invalid_named_voice_audio')
    if type(audio['seconds']) not in (int, float) or not math.isfinite(audio['seconds']) or audio['seconds'] <= 0:
        raise ValueError('invalid_named_voice_audio')
    return result
