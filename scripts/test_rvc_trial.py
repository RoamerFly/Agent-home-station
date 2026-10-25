"""Synthetic RVC dataset manifest checks; no real voice, models or network."""
from pathlib import Path
import tempfile
import unittest
from rvc_train_trial import training_filelist


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


if __name__=='__main__':unittest.main()
