"""Synthetic RVC dataset manifest checks; no real voice, models or network."""
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch, MagicMock
import json
from rvc_train_trial import training_filelist, resume_experiment, training_layout, create_shadow, fix_shadow_preprocess, main


class RVCManifestTests(unittest.TestCase):
    def test_zero_exit_without_export_is_failed_not_completed(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);(root/'train').mkdir();(root/'train/train.py').write_text('# synthetic')
            argv=['trial','--python',str(root/'venv/bin/python'),'--project',tmp,'--dataset',tmp,'--experiment','synthetic','--stage','train','--device','cuda:0']
            child=MagicMock();child.pid=123;child.wait.return_value=0
            with patch('sys.argv',argv), patch('rvc_train_trial.subprocess.check_output',return_value='{"cuda":true,"build":"test","name":"synthetic"}\n'), patch('rvc_train_trial.subprocess.run'), patch('rvc_train_trial.subprocess.Popen',return_value=child) as popen:
                with self.assertRaisesRegex(ValueError,'did_not_export'):main()
            state=json.loads((root/'logs/synthetic/training-state.json').read_text())
            self.assertEqual(state['state'],'failed');self.assertFalse(state['optimizer_steps_verified'])
            self.assertEqual(popen.call_args.args[0][1:3],['-m','train.train'])
            self.assertEqual(popen.call_args.args[0][0],str(root/'venv/bin/python'))

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
            # Directory symlinks need privileges on Windows; layout parsing
            # itself is still tested there, while CI verifies asset reuse.
            import os
            if os.name=='nt':return
            target=root/'shadow';create_shadow(source,target)
            (target/'train/train.py').write_text('# changed private copy')
            self.assertEqual((source/'train/train.py').read_text(),'# synthetic')
            self.assertFalse((target/'assets/weights').is_symlink())
            self.assertTrue((target/'assets/pretrained_v2').is_symlink())
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
