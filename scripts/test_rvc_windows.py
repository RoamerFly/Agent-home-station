import unittest
import numpy as np
from rvc_windows import convert_vocal_windows, conversion_parameters


class VocalWindowTests(unittest.TestCase):
    def test_context_removed_tail_not_padded_or_repeated_and_every_sample_retained(self):
        for size in [1,16000,128000,128001,2238400]:
            source=np.arange(size,dtype=np.float32);lengths=[];progress=[]
            def converter(part):lengths.append(len(part));return 16000,part
            rate,result=convert_vocal_windows(source,converter,progress=progress.append)
            self.assertEqual(rate,16000);np.testing.assert_array_equal(result,source)
            self.assertTrue(all(n==16000*10 for n in lengths))
            self.assertEqual(progress[-1]['completed_samples'],size)

    def test_output_rate_grid_has_no_rounding_drift_between_windows(self):
        source=np.ones(16000*61+31,dtype=np.float32)
        for target_rate in [32000,40000,44100,48000]:
            rate,result=convert_vocal_windows(source,lambda x:(target_rate,np.ones(round(len(x)*target_rate/16000),dtype=np.int16)),chunk_seconds=2.3,context_seconds=.7)
            self.assertEqual(rate,target_rate);self.assertEqual(len(result),round(len(source)*target_rate/16000))

    def test_defaults_are_adjustable_and_zero_uses_one_original_call(self):
        audio=np.arange(320001,dtype=np.float32);calls=[]
        def convert(x):calls.append(x.copy());return 16000,x
        _,result=convert_vocal_windows(audio,convert,chunk_seconds=0)
        np.testing.assert_array_equal(result,audio);self.assertEqual(len(calls),1)
        calls.clear();convert_vocal_windows(audio,convert,chunk_seconds=15,context_seconds=2)
        self.assertEqual(len(calls),2);self.assertEqual(len(calls[0]),16000*19)

    def test_model_padding_is_not_required_for_real_core_but_short_output_is_rejected(self):
        audio=np.ones(16000*2)
        _,result=convert_vocal_windows(audio,lambda x:(16000,x[:-320]))
        self.assertEqual(len(result),len(audio))
        with self.assertRaisesRegex(ValueError,'incomplete'):
            convert_vocal_windows(audio,lambda x:(16000,np.zeros(100)))

    def test_invalid_params_nonfinite_and_changing_sample_rates_rejected(self):
        for chunk,context in [(True,1),(-1,1),(1,float('inf')),(float('nan'),1),(.0000001,1)]:
            with self.assertRaises(ValueError):conversion_parameters(chunk,context)
        with self.assertRaisesRegex(ValueError,'finite'):convert_vocal_windows(np.array([float('nan')]),lambda x:(16000,x))
        rates=iter([32000,40000])
        with self.assertRaisesRegex(ValueError,'sample_rate_changed'):
            convert_vocal_windows(np.zeros(16000*10),lambda x:(next(rates),np.zeros(16000*30)))


if __name__=='__main__':unittest.main()
