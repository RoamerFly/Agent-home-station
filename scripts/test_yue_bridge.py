"""Synthetic workflow tests; no ComfyUI, models, private audio or network."""
import base64
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from yue_bridge import prepare_song_graph, song_options

WORKFLOWS = Path(__file__).resolve().parents[1] / 'components/yue2/app/workflows_api'

class SongWorkflowTests(unittest.TestCase):
    def body(self):
        return dict(title='合成测试',mode='original',style='soft female folk',lyrics='[Verse]\n合成歌词',seed=0,duration_seconds=10)

    def test_original_preserves_style_lyrics_and_fixed_zero_seed(self):
        body=self.body()
        with tempfile.TemporaryDirectory() as tmp:
            a=prepare_song_graph(WORKFLOWS,body,'synthetic',Path(tmp))
            b=prepare_song_graph(WORKFLOWS,body,'synthetic',Path(tmp))
        self.assertEqual(a,b)
        self.assertEqual(a['33:34']['inputs']['seed'],0)
        self.assertEqual(a['33:36']['inputs']['value'],body['style'])
        self.assertEqual(a['33:37']['inputs']['value'],body['lyrics'])
        self.assertEqual(a['33:25']['inputs']['max_duration'],10)

    def test_cover_uses_reference_modes_seed_and_unmodified_words(self):
        for mode in ('melody','full'):
            body=self.body();body.update(mode='cover',reference_mode=mode,reference_base64=base64.b64encode(b'synthetic audio').decode(),seed=123)
            with tempfile.TemporaryDirectory() as tmp,patch('yue_bridge.subprocess.run') as run:
                graph=prepare_song_graph(WORKFLOWS,body,'synthetic',Path(tmp))
                self.assertEqual((Path(tmp)/'wechat-ref-synthetic.audio').read_bytes(),b'synthetic audio')
                self.assertEqual(run.call_args.args[0][-1],str(Path(tmp)/'wechat-ref-synthetic.wav'))
            self.assertEqual(graph['45']['inputs']['audio'],'wechat-ref-synthetic.wav')
            self.assertEqual(graph['33:41']['inputs']['mode'],mode)
            self.assertEqual(graph['33:25']['inputs']['mode'],mode)
            self.assertEqual(graph['33:25']['inputs']['lyrics'],body['lyrics'])
            self.assertEqual(graph['33:25']['inputs']['style'],body['style'])
            self.assertEqual(graph['33:34']['inputs']['seed'],123)

    def test_invalid_modes_reference_and_seed_are_not_silently_original(self):
        for patch_values in ({'mode':'cover'},{'seed':True},{'seed':-1},{'seed':2147483648},{'reference_mode':'guess'},{'reference_base64':'x'},{'mode':'unknown'}):
            body=self.body();body.update(patch_values)
            with self.assertRaises(ValueError):song_options(body)
        body=self.body();body.update(mode='cover',reference_base64='invalid%%')
        with tempfile.TemporaryDirectory() as tmp,self.assertRaises(ValueError):
            prepare_song_graph(WORKFLOWS,body,'synthetic',Path(tmp))

if __name__=='__main__':unittest.main()
