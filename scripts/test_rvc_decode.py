from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
from rvc_voice import decode_audio_pcm


class RVCDecodeTests(unittest.TestCase):
    def test_local_file_decodes_as_mono_float_pcm(self):
        with tempfile.TemporaryDirectory() as d:
            file=Path(d)/'synthetic.wav';file.write_bytes(b'synthetic')
            with patch('rvc_voice.subprocess.run',return_value=subprocess.CompletedProcess([],0,b'\0'*8)) as run:
                self.assertEqual(len(decode_audio_pcm(file,16000)),8)
                argv=run.call_args.args[0]
                self.assertIn('f32le',argv);self.assertEqual(argv[-1],'pipe:1')
            with patch('rvc_voice.subprocess.run',return_value=subprocess.CompletedProcess([],0,b'bad')):
                with self.assertRaisesRegex(ValueError,'invalid_decoded_pcm'):decode_audio_pcm(file,16000)
        with self.assertRaisesRegex(ValueError,'invalid_audio_source'):decode_audio_pcm('https://example.org/audio',16000)


if __name__=='__main__':unittest.main()
