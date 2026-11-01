"""Named speech protocol and real WAV publication with synthetic dependencies."""
import copy
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import wave


ROOT = Path(__file__).resolve().parents[1]
def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module

main = load('named_speech_main', ROOT / 'runtime/main.py')
runner = load('named_speech_runner', ROOT / 'runtime/gpt_sovits.py')
CONFIG = {'local_profile_id': 'gpt-sovits'}
ARGS = {'voice': {'id': 'synthetic-voice', 'version': 'a' * 64}, 'text': '命名声线说话',
        'device': 'cuda:0', 'version': 'v2', 'parameters': {'sample_steps': 64}, 'timeout_seconds': 19.5}
RESULT = {'status': 'synthesized', 'audio': {'ref': 'audio_0', 'sha256': 'b' * 64,
          'bytes': 100, 'sample_rate': 24000, 'seconds': .25},
          'voice': ARGS['voice'], 'device': ARGS['device'], 'version': ARGS['version']}


class NamedVoiceTests(unittest.TestCase):
    def test_exact_request_response_and_independent_local_configuration(self):
        calls = []
        result = copy.deepcopy(RESULT)
        args = copy.deepcopy(ARGS)
        args['text'] = '长' * 9000
        out = main.named_voice.synthesize(args, CONFIG, lambda value: calls.append(value) or result)
        self.assertIs(out, result)
        self.assertEqual(calls, [args])
        self.assertEqual(out['voice']['version'], 'a' * 64)
        self.assertNotIn('tts_key', json.dumps(calls))
        self.assertNotIn('path', json.dumps(out))
        for config in ({}, {'local_profile_id': ''}, {'local_profile_id': 'other'}):
            with self.subTest(config=config), self.assertRaises(ValueError):
                main.named_voice.synthesize(ARGS, config, lambda _: self.fail('host called'))

    def test_invalid_args_fail_before_host_without_parameter_whitelist(self):
        mutations = [lambda a: a.update(output='/private/result.wav'),
                     lambda a: a['voice'].update(path='/private/weights'),
                     lambda a: a['voice'].update(version='v2'),
                     lambda a: a.update(timeout_seconds=0),
                     lambda a: a.update(timeout_seconds=True),
                     lambda a: a.update(timeout_seconds=float('nan')),
                     lambda a: a.update(device='other'),
                     lambda a: a.update(version='other'),
                     lambda a: a.update(text='')]
        for mutate in mutations:
            args = copy.deepcopy(ARGS)
            mutate(args)
            with self.subTest(args=args), self.assertRaises(ValueError):
                main.named_voice.synthesize(args, CONFIG, lambda _: self.fail('host called'))
        tuning = {'new_engine_option': {'mode': 'quality', 'strength': 1.25}, 'top_k': 42,
                  'batch_size': 8, 'text_lang': 'auto', 'seed': 123, 'speed_factor': .8}
        self.assertEqual(runner.validate_parameters(tuning), tuning)
        for key in ('text', 'prompt_text', 'ref_audio_path', 'aux_ref_audio_paths', 'gpt',
                    'sovits', 't2s_weights_path', 'vits_weights_path', 'output', 'device',
                    'version', 'streaming_mode', 'gpt_streaming', 'return_fragment', 'configFile'):
            with self.subTest(key=key), self.assertRaises(ValueError):
                runner.validate_parameters({key: False})
        for value in ({'new': {'x': '/private/model'}}, {'new': 'https://remote.test/x'},
                      {'new': ['C:\\private\\voice.pth']}, {'new': float('inf')}):
            with self.subTest(value=value), self.assertRaises(ValueError):
                runner.validate_parameters(value)

    def test_host_failure_extra_fields_and_false_bindings_are_rejected(self):
        changes = [lambda r: r.update(path='/private/result.wav'),
                   lambda r: r.update(error='failed'),
                   lambda r: r.update(status='accepted'),
                   lambda r: r.update(device='cpu'),
                   lambda r: r.update(version='v3'),
                   lambda r: r.update(voice={'id': 'different', 'version': 'a' * 64}),
                   lambda r: r['audio'].update(ref='/private/output.wav'),
                   lambda r: r['audio'].update(sha256='bad'),
                   lambda r: r['audio'].update(bytes=True),
                   lambda r: r['audio'].update(seconds=float('nan')),
                   lambda r: r['audio'].update(path='/private/output.wav')]
        for change in changes:
            result = copy.deepcopy(RESULT)
            change(result)
            calls = []
            with self.subTest(result=result), self.assertRaises(ValueError):
                main.named_voice.synthesize(ARGS, CONFIG, lambda a: calls.append(a) or result)
            self.assertEqual(len(calls), 1)

    def test_stdio_uses_only_scoped_local_speech_service(self):
        context = {k: 'synthetic' for k in ('account', 'thread', 'requester', 'purpose', 'request_key')}
        messages = [
            {'jsonrpc': '2.0', 'id': 1, 'method': 'initialize', 'params': {'_meta': {'wechat/configuration': {'values': CONFIG}}}},
            {'jsonrpc': '2.0', 'id': 2, 'method': 'tools/call', 'params': {'name': 'speech.synthesize_named', 'arguments': ARGS, '_meta': {'wechat/context': context}}},
            {'jsonrpc': '2.0', 'id': 'speech-model-1', 'result': RESULT},
        ]
        output = io.StringIO()
        main.Wire(io.StringIO('\n'.join(json.dumps(row) for row in messages) + '\n'), output).serve()
        rows = [json.loads(row) for row in output.getvalue().splitlines()]
        self.assertEqual(rows[0]['result']['serverInfo']['version'], '0.2.0')
        self.assertEqual(rows[1]['method'], 'wechat/local_speech')
        self.assertEqual(rows[1]['params'], ARGS)
        self.assertEqual(rows[2]['result'], RESULT)
        self.assertNotIn('wechat/model_request', output.getvalue())

    def test_package_independent_configuration_and_no_direct_process_permission(self):
        package = json.loads((ROOT / 'plugin.json').read_text(encoding='utf8'))
        config = json.loads((ROOT / 'config.schema.json').read_text(encoding='utf8'))
        caps = json.loads((ROOT / 'capabilities.json').read_text(encoding='utf8'))['capabilities']
        self.assertEqual(package['version'], '0.2.0')
        self.assertEqual(package['permissions'], ['artifact.read', 'model.invoke'])
        self.assertTrue(package['http_routes'][0]['optional'])
        self.assertFalse(config.get('required'))
        self.assertEqual(config['properties']['local_profile_id']['default'], '')
        cap = next(c for c in caps if c['id'] == 'speech.synthesize_named')
        self.assertEqual(cap['permissions'], ['artifact.read', 'model.invoke'])
        self.assertEqual(cap['implementation'], 'ready')
        self.assertTrue(cap['discoverable'])


class RunnerTests(unittest.TestCase):
    def fixture(self, root):
        for name in ('project/GPT_SoVITS/TTS_infer_pack/TTS.py',
                     'models/chinese-roberta-wwm-ext-large/config.json',
                     'models/chinese-hubert-base/config.json', 'gpt.ckpt', 'sovits.pth', 'reference.wav'):
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(('synthetic-' + name).encode('utf8'))
        prompt = root / 'prompt.txt'
        prompt.write_text('\ufeff 参考文本\n', encoding='utf8')
        return runner.parser().parse_args(['--project', str(root / 'project'), '--models', str(root / 'models'),
            '--gpt', str(root / 'gpt.ckpt'), '--sovits', str(root / 'sovits.pth'),
            '--reference', str(root / 'reference.wav'), '--prompt-text-file', str(prompt),
            '--text', '原始正文', '--device', 'cpu', '--output', str(root / 'speech.wav')])

    def fake_modules(self, calls, mutate=None, cuda=False):
        class Config:
            def __init__(self, value):
                self.version = value['custom']['version']
                calls['config'] = value
        class TTS:
            def __init__(self, config):
                self.configs = config
            def run(self, request):
                calls['request'] = request
                if mutate:
                    mutate()
                yield 24000, [0] * 240
                yield 24000, [0] * 240
        def write(path, audio, rate, subtype):
            with wave.open(str(path), 'wb') as output:
                output.setparams((1, 2, rate, 0, 'NONE', 'not compressed'))
                output.writeframes(b'\x00\x00' * len(audio))
        def info(path):
            with wave.open(str(path), 'rb') as source:
                return SimpleNamespace(frames=source.getnframes(), duration=source.getnframes()/source.getframerate())
        return {'torch': SimpleNamespace(cuda=SimpleNamespace(is_available=lambda: cuda)),
                'TTS_infer_pack.TTS': SimpleNamespace(TTS=TTS, TTS_Config=Config, torchaudio=SimpleNamespace(load=lambda *a, **kw: None)),
                'numpy': SimpleNamespace(concatenate=lambda chunks: sum(chunks, [])),
                'soundfile': SimpleNamespace(write=write, info=info)}

    def run_synthetic(self, args, calls, mutate=None):
        cwd, path = os.getcwd(), sys.path[:]
        try:
            with patch.dict(sys.modules, self.fake_modules(calls, mutate)), patch.dict(os.environ, {}, clear=False):
                return runner.synthesize(args)
        finally:
            os.chdir(cwd)
            sys.path[:] = path

    def test_actual_wav_receipt_binds_inputs_and_has_no_parameter_path_dump(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            args = self.fixture(root)
            args.parameters = root / 'parameters.json'
            args.parameters.write_text('{"sample_steps":64,"new_engine_option":true}', encoding='utf8')
            calls = {}
            result = self.run_synthetic(args, calls)
            with wave.open(str(args.output), 'rb') as audio:
                self.assertEqual(audio.getnframes(), 480)
            for key, filename in (('gpt_sha256', 'gpt.ckpt'), ('sovits_sha256', 'sovits.pth'), ('reference_sha256', 'reference.wav'), ('sha256', 'speech.wav')):
                self.assertEqual(result[key], hashlib.sha256((root / filename).read_bytes()).hexdigest())
            self.assertEqual(result['prompt_sha256'], hashlib.sha256('参考文本'.encode('utf8')).hexdigest())
            self.assertEqual(result['text_sha256'], hashlib.sha256('原始正文'.encode('utf8')).hexdigest())
            self.assertEqual(result['wechat_sends'], 0)
            self.assertEqual(result['sample_rate'], 24000)
            self.assertEqual(result['seconds'], .02)
            self.assertEqual(result['device'], 'cpu')
            self.assertNotIn('effective_parameters', result)
            self.assertEqual(calls['request']['sample_steps'], 64)
            self.assertTrue(calls['request']['new_engine_option'])

    def test_modified_model_during_inference_is_not_published(self):
        with tempfile.TemporaryDirectory() as directory:
            args = self.fixture(Path(directory))
            with self.assertRaisesRegex(ValueError, 'synthesis_input_changed'):
                self.run_synthetic(args, {}, lambda: args.gpt.write_bytes(b'replaced'))
            self.assertFalse(args.output.exists())

    def test_requested_cuda_unavailable_does_not_use_cpu(self):
        with tempfile.TemporaryDirectory() as directory:
            args = self.fixture(Path(directory))
            args.device = 'cuda'
            calls = {}
            with self.assertRaisesRegex(RuntimeError, 'cuda_unavailable'):
                self.run_synthetic(args, calls)
            self.assertFalse(calls)
            self.assertFalse(args.output.exists())


if __name__ == '__main__':
    unittest.main()
