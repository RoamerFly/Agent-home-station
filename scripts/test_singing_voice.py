"""Synthetic mixing contracts; no model, provider, reference audio or network."""
import unittest
import numpy as np
from singing_voice import mix_converted


class SingingMixTests(unittest.TestCase):
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
