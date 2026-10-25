"""Synthetic mixing contracts; no model, provider, reference audio or network."""
import unittest
from pathlib import Path
import tempfile
import hashlib
from unittest.mock import patch
import numpy as np
from singing_voice import mix_converted,validate_singing_config


class SingingMixTests(unittest.TestCase):
    def test_seed_quality_parameters_are_bounded(self):
        with tempfile.TemporaryDirectory() as tmp:
            ref=Path(tmp)/'ref.wav';ref.write_bytes(b'synthetic reference')
            c=dict(engine='seed-vc',reference=str(ref),reference_sha256=hashlib.sha256(ref.read_bytes()).hexdigest(),runtime=tmp,source_revision='synthetic-revision',diffusion_steps=50,inference_cfg_rate=.9)
            with patch('singing_voice.subprocess.check_output',return_value='synthetic-revision\n'):
                self.assertEqual(validate_singing_config(c),'seed-vc')
            for steps,cfg in [(True,.7),(0,.7),(101,.7),(50,float('nan')),(50,2)]:
                c.update(diffusion_steps=steps,inference_cfg_rate=cfg)
                with self.assertRaisesRegex(ValueError,'invalid_seed_parameters'):validate_singing_config(c)

    def test_rvc_uses_trained_model_and_index_without_seed_vc_reference(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);model=root/'voice.pth';index=root/'voice.index';model.write_bytes(b'synthetic model');index.write_bytes(b'synthetic index')
            config=dict(engine='rvc',rvc_python=str(model),rvc_project=str(root),rvc_model=str(model),rvc_index=str(index),rvc_model_sha256=hashlib.sha256(model.read_bytes()).hexdigest(),rvc_index_sha256=hashlib.sha256(index.read_bytes()).hexdigest(),index_rate=.35,semitones=0)
            self.assertEqual(validate_singing_config(config),'rvc')
            config['index_rate']=True
            with self.assertRaisesRegex(ValueError,'parameters'):validate_singing_config(config)
            config['index_rate']=.35;config['precision']='float64'
            with self.assertRaisesRegex(ValueError,'precision'):validate_singing_config(config)
            config['precision']='float32'
            self.assertEqual(validate_singing_config(config),'rvc')
            config['index_rate']=.35;index.write_bytes(b'changed index')
            with self.assertRaisesRegex(ValueError,'resource_changed'):validate_singing_config(config)

    def test_key_changes_must_preserve_harmony(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);model=root/'voice.pth';index=root/'voice.index';model.write_bytes(b'model');index.write_bytes(b'index')
            c=dict(engine='rvc',rvc_python=str(model),rvc_project=str(root),rvc_model=str(model),rvc_index=str(index),rvc_model_sha256=hashlib.sha256(model.read_bytes()).hexdigest(),rvc_index_sha256=hashlib.sha256(index.read_bytes()).hexdigest())
            for voice,backing in [(0,0),(12,0),(7,7),(-5,7)]:
                c.update(semitones=voice,accompaniment_semitones=backing)
                self.assertEqual(validate_singing_config(c),'rvc')
            for voice,backing in [(7,0),(3,True),(13,1)]:
                c.update(semitones=voice,accompaniment_semitones=backing)
                with self.assertRaises(ValueError):validate_singing_config(c)

    def test_explicit_accompaniment_does_not_reintroduce_original_singer(self):
        original=np.full((10000,2),.3,dtype=np.float32)
        output=mix_converted(original,np.full_like(original,.2),np.full(10000,.3),np.full_like(original,.05))
        np.testing.assert_allclose(output,.35,atol=1e-6)
        with self.assertRaisesRegex(ValueError,'shape_mismatch'):mix_converted(original,original,np.ones(10000),np.zeros((100,2)))

    def test_replace_vocal_preserves_accompaniment_and_duration(self):
        accompaniment=np.full((10000,2),.1,dtype=np.float32)
        vocals=np.full((10000,2),.2,dtype=np.float32)
        converted=np.full(10000,.3,dtype=np.float32)
        output=mix_converted(accompaniment+vocals,vocals,converted)
        self.assertEqual(output.shape,(10000,2))
        np.testing.assert_allclose(output,.4,atol=1e-6)

    def test_large_timing_drift_and_nonfinite_samples_are_rejected(self):
        original=np.zeros((10000,2),dtype=np.float32)
        with self.assertRaisesRegex(ValueError,'duration_mismatch'):
            mix_converted(original,original,np.ones(5000))
        with self.assertRaisesRegex(ValueError,'invalid_converted'):
            mix_converted(original,original,np.full(10000,np.nan))

    def test_codec_padding_is_bounded_and_clipping_is_prevented(self):
        original=np.full((10000,2),.2,dtype=np.float32)
        output=mix_converted(original,np.zeros_like(original),np.full(10010,2.))
        self.assertEqual(len(output),len(original))
        self.assertLessEqual(float(np.abs(output).max()),.981)


if __name__=='__main__':unittest.main()
