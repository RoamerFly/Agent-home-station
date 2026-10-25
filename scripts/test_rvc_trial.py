"""Synthetic RVC dataset manifest checks; no real voice, models or network."""
from pathlib import Path
import tempfile
import unittest
from rvc_train_trial import training_filelist, resume_experiment


class RVCManifestTests(unittest.TestCase):
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
