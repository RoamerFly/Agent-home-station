import unittest
import numpy as np
from rvc_pitch import pitch_parameters, pitch_windows, infer_pitch_windows, install_windowed_rmvpe


class PitchWindowTests(unittest.TestCase):
    def test_frame_grid_covers_every_core_once_including_endpoint_and_tail(self):
        for samples in [160,161,159999,160000,160001,320000,2238400]:
            audio=np.arange(samples,dtype=np.float32)
            seen=[]
            def infer(part):
                # A global frame identity makes any reordering, missing frame,
                # duplicate edge or context accidentally copied visible.
                first=int(part[0])//160
                return np.arange(first, first+len(part)//160+1,dtype=np.float32)
            result=infer_pitch_windows(audio,infer,progress=seen.append)
            np.testing.assert_array_equal(result,np.arange(samples//160+1))
            self.assertEqual(seen[-1]['completed_frames'],samples//160+1)
            self.assertEqual(seen[-1]['total_frames'],len(result))
            windows=list(pitch_windows(samples,pitch_parameters()))
            self.assertEqual(windows[0]['frame_start'],0)
            self.assertEqual(windows[-1]['sample_end'],samples)
            for a,b in zip(windows,windows[1:]):self.assertEqual(a['frame_end'],b['frame_start'])

    def test_default_slice_limits_each_model_input_but_not_recording_duration(self):
        spans=list(pitch_windows(16000*3600,pitch_parameters()))
        self.assertEqual(spans[-1]['frame_end'],360001)
        self.assertEqual(len(spans),360)
        self.assertLessEqual(max(w['sample_end']-w['sample_start'] for w in spans),16000*12)
        large=list(pitch_windows(16000*300,pitch_parameters(75,3)))
        self.assertEqual(len(large),4)
        unchunked=list(pitch_windows(16000*300,pitch_parameters(0,0)))
        self.assertEqual(len(unchunked),1)
        self.assertEqual(unchunked[0]['sample_end'],16000*300)

    def test_explicit_settings_and_threshold_reach_model_and_short_audio_unchanged(self):
        class Fake:
            def __init__(self):self.calls=[]
            def infer_from_audio(self,audio,thred=.03):
                self.calls.append((len(audio),thred))
                return np.ones(len(audio)//160+1,dtype=np.float32)
        options=install_windowed_rmvpe(Fake,2,.5)
        f=Fake();result=f.infer_from_audio(np.zeros(16000*7),thred=.2)
        self.assertEqual(options['chunk_seconds'],2)
        self.assertEqual(len(result),701)
        self.assertEqual(len(f.calls),4)
        self.assertTrue(all(n<=16000*3 and threshold==.2 for n,threshold in f.calls))
        f.calls.clear();short=f.infer_from_audio(np.zeros(8000),thred=.4)
        self.assertEqual(len(short),51);self.assertEqual(f.calls,[(8000,.4)])
        install_windowed_rmvpe(Fake,0,0)
        f.calls.clear();f.infer_from_audio(np.zeros(16000*7),thred=.5)
        self.assertEqual(f.calls,[(16000*7,.5)])

    def test_zero_overlap_and_fractional_frame_alignment_report_effective_values(self):
        for size in [320000,320001,320500]:
            result=infer_pitch_windows(np.arange(size,dtype=np.float32),lambda x:np.arange(int(x[0])//160,int(x[0])//160+len(x)//160+1),chunk_seconds=2,overlap_seconds=0)
            np.testing.assert_array_equal(result,np.arange(size//160+1))
        self.assertAlmostEqual(pitch_parameters(2.024,.314)['chunk_seconds'],2.02)

    def test_invalid_parameters_and_partial_or_nonfinite_results_fail_explicitly(self):
        for chunk,overlap in [(True,1),(-1,1),(float('inf'),1),(.001,1),(1,float('nan')),(1,-1)]:
            with self.assertRaises(ValueError):pitch_parameters(chunk,overlap)
        for value in [np.zeros(1),np.full(200,float('nan')),np.full(200,-1),np.zeros((2,200))]:
            with self.assertRaisesRegex(ValueError,'result_invalid'):infer_pitch_windows(np.zeros(16000),lambda x:value)


if __name__=='__main__':unittest.main()
