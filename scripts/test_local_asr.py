import unittest
from types import SimpleNamespace
from local_asr import clean_text, speech_detected


class LocalASRTests(unittest.TestCase):
    def test_non_speech_events_never_become_invented_transcript(self):
        self.assertEqual(clean_text("<|zh|><|NEUTRAL|><|BGM|><|woitn|>"), "")
        self.assertEqual(clean_text("<|zh|><|NEUTRAL|>你好，今天怎么样？<|Speech|>"), "你好，今天怎么样？")
        self.assertEqual(clean_text("plain text"), "plain text")

    def test_vad_empty_result_is_not_replaced_by_a_word_filter(self):
        class Config:
            def __init__(self):
                self.silero_vad=SimpleNamespace(window_size=3)
        class Detector:
            def __init__(self,config,buffer_size_in_seconds):
                self.received=[]
            def accept_waveform(self,samples):
                self.received.extend(samples)
            def empty(self):
                return True
            def flush(self):
                pass
        sdk=SimpleNamespace(VadModelConfig=Config,VoiceActivityDetector=Detector)
        args=SimpleNamespace(vad_model='synthetic',vad_threshold=.5,vad_min_speech=.25,threads=2,chunk_seconds=30)
        self.assertFalse(speech_detected([0]*7,args,sdk))
        args.vad_model=None
        self.assertTrue(speech_detected([0]*7,args,sdk))
        # A real short acknowledgement is retained when speech is detected.
        self.assertEqual(clean_text('嗯'),'嗯')


if __name__ == "__main__":
    unittest.main()
