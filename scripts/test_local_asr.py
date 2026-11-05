import importlib.util
from pathlib import Path
import unittest
from types import SimpleNamespace


ROOT = Path(__file__).resolve().parents[1]
RUNTIME = ROOT / "plugins" / "media-understanding" / "runtime" / "local_asr.py"
SHIM = ROOT / "scripts" / "local_asr.py"
INSTALLER = ROOT / "scripts" / "install_local_asr.py"

runtime_spec = importlib.util.spec_from_file_location("media_local_asr", RUNTIME)
runtime = importlib.util.module_from_spec(runtime_spec)
runtime_spec.loader.exec_module(runtime)
shim_spec = importlib.util.spec_from_file_location("local_asr_compat", SHIM)
shim = importlib.util.module_from_spec(shim_spec)
shim_spec.loader.exec_module(shim)
installer_spec = importlib.util.spec_from_file_location("local_asr_installer", INSTALLER)
installer = importlib.util.module_from_spec(installer_spec)
installer_spec.loader.exec_module(installer)

clean_text = runtime.clean_text
speech_detected = runtime.speech_detected


class LocalASRTests(unittest.TestCase):
    def test_compatibility_shim_targets_only_packaged_runtime(self):
        self.assertEqual(shim.plugin_entry().resolve(), RUNTIME.resolve())

    def test_installer_deploys_the_same_plugin_owned_runner(self):
        self.assertEqual(installer.plugin_runtime_entry().resolve(), RUNTIME.resolve())

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
