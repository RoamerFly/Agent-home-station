import json
from pathlib import Path
import tempfile
import subprocess
import unittest

from gpt_sovits_tts import parser, prepare
from register_gpt_sovits import copy_verified, register


class GPTSoVITSTests(unittest.TestCase):
    def fixture(self, directory):
        root = Path(directory)
        for name in ['project/GPT_SoVITS/TTS_infer_pack/TTS.py','models/chinese-roberta-wwm-ext-large/config.json',
                     'models/chinese-hubert-base/config.json','voice.ckpt','voice.pth','ref.wav']:
            path = root/name
            path.parent.mkdir(parents=True,exist_ok=True)
            path.write_text('synthetic')
        return parser().parse_args(['--project',str(root/'project'),'--models',str(root/'models'),
            '--gpt',str(root/'voice.ckpt'),'--sovits',str(root/'voice.pth'),
            '--reference',str(root/'ref.wav'),'--prompt-text','参考录音中的话',
            '--text','你今天刷牙了吗？','--output',str(root/'output.wav')])

    def test_missing_explicit_weight_never_falls_back(self):
        with tempfile.TemporaryDirectory() as tmp:
            args = self.fixture(tmp)
            args.gpt.unlink()
            with self.assertRaisesRegex(ValueError,'required_inference_file_missing'):
                prepare(args)

    def test_parameters_forwarded_without_clamping_and_prompt_is_reference(self):
        with tempfile.TemporaryDirectory() as tmp:
            args = self.fixture(tmp)
            args.parameters = Path(tmp)/'params.json'
            args.parameters.write_text(json.dumps({'top_k':42,'seed':123,'batch_size':8,'sample_steps':64,'speed_factor':0.8}))
            result = prepare(args)
            self.assertEqual(result['top_k'],42)
            self.assertEqual(result['batch_size'],8)
            self.assertEqual(result['sample_steps'],64)
            self.assertEqual(result['prompt_text'],'参考录音中的话')
            self.assertEqual(result['text'],'你今天刷牙了吗？')

    def test_output_collision_and_streaming_are_explicit(self):
        with tempfile.TemporaryDirectory() as tmp:
            args=self.fixture(tmp)
            args.output.write_text('keep')
            with self.assertRaises(FileExistsError):
                prepare(args)
            self.assertEqual(args.output.read_text(),'keep')
            args.overwrite=True
            args.parameters=Path(tmp)/'stream.json'
            args.parameters.write_text('{"streaming_mode":true}')
            with self.assertRaisesRegex(ValueError,'streaming_requires_upstream_api_v2'):
                prepare(args)

    def test_import_hash_and_no_existing_asset_replacement(self):
        with tempfile.TemporaryDirectory() as tmp:
            source,target=Path(tmp)/'src',Path(tmp)/'dst'
            source.write_bytes(b'synthetic-base-model')
            copy_verified(source,target)
            copy_verified(source,target)
            source.write_bytes(b'different')
            with self.assertRaisesRegex(ValueError,'existing_managed_asset_differs'):
                copy_verified(source,target)
            self.assertEqual(target.read_bytes(),b'synthetic-base-model')

    def test_registration_imports_code_and_bases_without_personal_models_or_config(self):
        with tempfile.TemporaryDirectory() as tmp:
            source,public=Path(tmp)/'source',Path(tmp)/'public'
            source.mkdir()
            files={
                'api_v2.py':'# synthetic',
                'GPT_SoVITS/text/chinese2.py':'import os\nmodel_dir="GPT_SoVITS/text/G2PWModel"\n',
                'GPT_SoVITS/text/LangSegmenter/langsegmenter.py':'import logging\n',
                'GPT_SoVITS/text/english.py':'import os\n',
                'GPT_SoVITS/text/g2pw/g2pw.py':'import os\n',
                'tools/portable_runtime.py':'FFMPEG_EXE = RUNTIME_DIR / "ffmpeg.exe"\n',
                'GPT_SoVITS/configs/tts_infer.yaml':'synthetic-personal-config',
                'GPT_weights_v2/personal.ckpt':'synthetic-private-weight',
                'logs/personal-training.txt':'synthetic-private-training',
            }
            for name,text in files.items():
                file=source/name
                file.parent.mkdir(parents=True,exist_ok=True)
                file.write_text(text)
            for folder in ['chinese-roberta-wwm-ext-large','chinese-hubert-base']:
                file=source/'GPT_SoVITS/pretrained_models'/folder/'config.json'
                file.parent.mkdir(parents=True,exist_ok=True)
                file.write_text('{"synthetic":true}')
            subprocess.run(['git','init','-q',str(source)],check=True)
            subprocess.run(['git','-C',str(source),'add','.'],check=True)
            subprocess.run(['git','-C',str(source),'-c','user.name=Synthetic','-c','user.email=synthetic@example.invalid','commit','-qm','synthetic'],check=True)
            interpreter=public/'runtimes/example/bin/python'
            first=register(source,public,interpreter)
            root=Path(first['root'])
            self.assertTrue((root/'api_v2.py').is_file())
            self.assertFalse((root/'GPT_weights_v2').exists())
            self.assertFalse((root/'logs').exists())
            self.assertFalse((root/'GPT_SoVITS/configs/tts_infer.yaml').exists())
            before=(root/'tools/portable_runtime.py').read_bytes()
            self.assertEqual(register(source,public,interpreter),first)
            self.assertEqual((root/'tools/portable_runtime.py').read_bytes(),before)


if __name__=='__main__':
    unittest.main()
