"""Synthetic RVC dataset manifest checks; no real voice, models or network."""
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch, MagicMock
import json
import importlib.util
import sys

ENTRY = Path(__file__).resolve().parents[1] / 'runtime/training/rvc_train_trial.py'
SPEC = importlib.util.spec_from_file_location('music_rvc_training', ENTRY)
training = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(training)
sys.modules[SPEC.name] = training
training_filelist, resume_experiment, training_layout = training.training_filelist, training.resume_experiment, training.training_layout
create_shadow, fix_shadow_preprocess = training.create_shadow, training.fix_shadow_preprocess
training_python_command, verify_optimizer_steps, main = training.training_python_command, training.verify_optimizer_steps, training.main


class RVCManifestTests(unittest.TestCase):
    def test_prepare_and_train_persist_original_stage_and_export_evidence(self):
        from types import SimpleNamespace
        import hashlib
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root/'train').mkdir()
            (root/'train/train.py').write_text('# synthetic registered trainer')
            (root/'configs/v2').mkdir(parents=True)
            (root/'configs/v2/32k.json').write_text('{"train":{}}')
            experiment = root/'logs/selected-voice'
            commands = []

            def child(command, **kwargs):
                commands.append(command)
                self.assertEqual(kwargs['cwd'], root.absolute())
                if 'train.train' in command:
                    (root/'assets/weights').mkdir(parents=True)
                    (root/'assets/weights/selected-voice.pth').write_bytes(b'synthetic exported voice')
                    for name in ('G_2333333.pth', 'D_2333333.pth'):
                        (experiment/name).write_bytes(b'synthetic optimizer checkpoint')
                else:
                    for relative in ('0_gt_wavs/a.wav', '3_feature768/a.npy', '2a_f0/a.wav.npy', '2b-f0nsf/a.wav.npy'):
                        path = experiment/relative
                        path.parent.mkdir(parents=True, exist_ok=True)
                        path.write_bytes(b'synthetic complete sample')
                result = MagicMock()
                result.pid, result.wait.return_value = 123, 0
                return result

            arguments = ['trial', '--python', str(root/'registered-venv/python'), '--project', str(root),
                         '--dataset', str(root/'authorized-dataset'), '--experiment', 'selected-voice',
                         '--epochs', '3', '--device', 'cuda:0']
            with patch.object(training.subprocess, 'check_output', return_value='{"cuda":true,"build":"synthetic","name":"mock"}\n'), \
                 patch.object(training.subprocess, 'Popen', side_effect=child), \
                 patch.object(training.subprocess, 'run') as validate_export, \
                 patch.object(training, 'prepare_mute'), \
                 patch.dict('sys.modules', {'torch': SimpleNamespace(load=lambda *a, **kw: {'optimizer': {'state': {0: {'step': 7}}}})}):
                with patch('sys.argv', [*arguments, '--stage', 'prepare']):
                    main()
                with patch('sys.argv', [*arguments, '--stage', 'train']):
                    main()
            prepared = json.loads((experiment/'prepare-state.json').read_text())
            trained = json.loads((experiment/'training-state.json').read_text())
            self.assertEqual(prepared['state'], 'completed')
            self.assertEqual(prepared['complete_samples'], 1)
            self.assertEqual(trained['state'], 'completed')
            self.assertEqual(trained['device'], 'cuda:0')
            self.assertTrue(trained['optimizer_steps_verified'])
            self.assertEqual(trained['optimizer_steps'], {'generator_steps': 7, 'discriminator_steps': 7})
            self.assertEqual(trained['model_sha256'], hashlib.sha256(b'synthetic exported voice').hexdigest())
            self.assertEqual(validate_export.call_args.args[0][-1], '3epoch')
            self.assertEqual(len(commands), 4)

    def test_index_uses_original_experiment_and_persists_actual_vector_count(self):
        from types import SimpleNamespace
        class Features:
            ndim, shape = 2, (2, 768)
            def astype(self, dtype):
                return self
        class Index:
            ntotal = 0
            def add(self, features):
                self.ntotal = features.shape[0]
        numpy = SimpleNamespace(load=lambda *a, **kw: Features(), concatenate=lambda arrays: Features(),
                                isfinite=lambda features: SimpleNamespace(all=lambda: True))
        faiss = SimpleNamespace(IndexFlatL2=lambda dimensions: Index(),
                                serialize_index=lambda index: SimpleNamespace(tobytes=lambda: b'synthetic index'),
                                write_index=lambda index, path: Path(path).write_bytes(b'synthetic index'))
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root/'train').mkdir()
            (root/'train/train.py').write_text('# synthetic registered trainer')
            experiment = root/'logs/selected-voice'
            (experiment/'3_feature768').mkdir(parents=True)
            (experiment/'3_feature768/a.npy').write_bytes(b'synthetic features')
            arguments = ['trial', '--python', str(root/'registered-python'), '--project', str(root), '--dataset', str(root),
                         '--experiment', 'selected-voice', '--stage', 'index', '--device', 'cpu']
            with patch('sys.argv', arguments), patch.object(training.subprocess, 'check_output', return_value='{"cuda":false}\n'), \
                 patch.dict('sys.modules', {'numpy': numpy, 'faiss': faiss}):
                main()
            state = json.loads((experiment/'index-state.json').read_text())
            self.assertEqual(state['state'], 'completed')
            self.assertEqual(state['index_vectors'], 2)
            self.assertEqual((experiment/'selected-voice-flat.index').read_bytes(), b'synthetic index')

    def test_export_without_optimizer_updates_is_not_verified_training(self):
        from types import SimpleNamespace
        for state in ({},{0:{'step':0}}):
            with patch.dict('sys.modules',{'torch':SimpleNamespace(load=lambda *a,**kw:{'optimizer':{'state':state}})}):
                with self.assertRaisesRegex(ValueError,'optimizer_did_not_update'):
                    verify_optimizer_steps(Path('synthetic'))
        with patch.dict('sys.modules',{'torch':SimpleNamespace(load=lambda *a,**kw:{'optimizer':{'state':{0:{'step':7}}}})}):
            self.assertEqual(verify_optimizer_steps(Path('synthetic')),{'generator_steps':7,'discriminator_steps':7})

    def test_zero_exit_without_export_is_failed_not_completed(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);(root/'train').mkdir();(root/'train/train.py').write_text('# synthetic')
            argv=['trial','--python',str(root/'venv/bin/python'),'--project',tmp,'--dataset',tmp,'--experiment','synthetic','--stage','train','--device','cuda:0']
            child=MagicMock();child.pid=123;child.wait.return_value=0
            with patch('sys.argv',argv), patch('music_rvc_training.subprocess.check_output',return_value='{"cuda":true,"build":"test","name":"synthetic"}\n'), patch('music_rvc_training.subprocess.run'), patch('music_rvc_training.subprocess.Popen',return_value=child) as popen:
                with self.assertRaisesRegex(ValueError,'did_not_export'):main()
            state=json.loads((root/'logs/synthetic/training-state.json').read_text())
            self.assertEqual(state['state'],'failed');self.assertFalse(state['optimizer_steps_verified'])
            command=popen.call_args.args[0]
            self.assertEqual(command[command.index('train.train')-1],'-m')
            self.assertEqual(popen.call_args.args[0][0],str(root/'venv/bin/python'))

    def test_windows_embedded_bootstrap_preserves_module_imports_and_arguments(self):
        import os, subprocess, sys
        if os.name != 'nt':
            self.skipTest('Windows embedded Python bootstrap')
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);project=root/'中文 project';extra=root/'packages';project.mkdir();extra.mkdir()
            (extra/'synthetic_dependency.py').write_text('value=42')
            (project/'synthetic_train.py').write_text('import json,sys,synthetic_dependency;print(json.dumps([synthetic_dependency.value,sys.argv[1:]]))')
            arguments=['空 格', '', 'quoted"value', 'tail\\']
            command=training_python_command(sys.executable,project,[extra],['-m','synthetic_train',*arguments])
            result=subprocess.check_output(command,text=True)
            self.assertEqual(json.loads(result),[42,arguments])

    def test_every_silence_sliced_tail_is_retained_in_private_copy(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);(root/'train').mkdir();(root/'training-source.json').write_text('{}')
            source='''class PreProcess:
    def pipeline(self, chunks):
        if True:
            for tmp_audio in chunks:
                output_key,idx1='sample',0
            self.norm_write(tmp_audio, output_key, idx1)
            if should_report():
                pass
'''
            file=root/'train/preprocess.py';file.write_text(source)
            fix_shadow_preprocess(root)
            scope={'should_report':lambda:False};exec(file.read_text(),scope)
            values=[];obj=scope['PreProcess']();obj.norm_write=lambda audio,*_:values.append(audio)
            obj.pipeline([[1,2],[3,4]])
            self.assertEqual(values,[[1,2],[3,4]])

    def test_official_layout_and_shadow_keep_writes_private(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);source=root/'source';(source/'train').mkdir(parents=True)
            (source/'train/train.py').write_text('# synthetic')
            (source/'assets/pretrained_v2').mkdir(parents=True)
            (source/'assets/pretrained_v2/G.pth').write_bytes(b'synthetic readonly model')
            self.assertTrue(training_layout(source)['official'])
            import os
            target=root/'shadow';create_shadow(source,target)
            (target/'train/train.py').write_text('# changed private copy')
            self.assertEqual((source/'train/train.py').read_text(),'# synthetic')
            self.assertFalse((target/'assets/weights').is_symlink())
            self.assertEqual((target/'assets/pretrained_v2').is_symlink(), os.name!='nt')
            if os.name=='nt':
                (target/'assets/pretrained_v2/G.pth').write_bytes(b'private training update')
                self.assertEqual((source/'assets/pretrained_v2/G.pth').read_bytes(),b'synthetic readonly model')
            with self.assertRaisesRegex(ValueError,'must_be_new'):create_shadow(source,target)

    def test_requires_complete_features_and_pins_one_speaker(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            for directory in ['0_gt_wavs','3_feature768','2a_f0','2b-f0nsf']:(root/directory).mkdir()
            for relative in ['0_gt_wavs/a.wav','3_feature768/a.npy','2a_f0/a.wav.npy','2b-f0nsf/a.wav.npy','0_gt_wavs/incomplete.wav']:(root/relative).write_bytes(b'synthetic')
            rows=training_filelist(root,root/'mute')
            self.assertEqual(len(rows),3)
            self.assertEqual(rows,training_filelist(root,root/'mute'))
            self.assertTrue(all(row.endswith('|0')for row in rows))
            self.assertFalse(any('incomplete' in row for row in rows))

    def test_refuses_empty_dataset(self):
        with tempfile.TemporaryDirectory() as tmp:
            with self.assertRaisesRegex(ValueError,'no_complete_training_samples'):training_filelist(tmp,Path(tmp)/'mute')

    def test_resume_uses_new_independent_checkpoint_copies(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);source=root/'logs'/'old';source.mkdir(parents=True)
            names=['config.json','filelist.txt','G_2333333.pth','D_2333333.pth']
            for name in names:(source/name).write_bytes(b'synthetic checkpoint')
            target=resume_experiment(root,'old','new')
            (target/'G_2333333.pth').write_bytes(b'new training state')
            self.assertEqual((source/'G_2333333.pth').read_bytes(),b'synthetic checkpoint')
            with self.assertRaisesRegex(ValueError,'must_be_new'):resume_experiment(root,'old','new')
            with self.assertRaisesRegex(ValueError,'invalid_experiment'):resume_experiment(root,'../old','other')


if __name__=='__main__':unittest.main()
