"""Synthetic workflow tests; no ComfyUI, models, private audio or network."""
import base64
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from yue_bridge import prepare_song_graph, song_options, singing_profile, convert_singing_voice, song_recipe

WORKFLOWS = Path(__file__).resolve().parents[1] / 'components/yue2/app/workflows_api'

class SongWorkflowTests(unittest.TestCase):
    def test_fixed_voice_reference_and_queued_configuration_are_pinned(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);reference=root/'voice.wav';reference.write_bytes(b'synthetic voice')
            python=root/'python';python.write_bytes(b'synthetic executable')
            config=root/'voice.json'
            data=dict(enabled=True,engine='rvc',profile='synthetic-C',python=str(python),runtime=str(root),rvc_python=str(python),rvc_project=str(root),rvc_model=str(reference),rvc_index=str(reference),rvc_model_sha256=hashlib.sha256(reference.read_bytes()).hexdigest(),rvc_index_sha256=hashlib.sha256(reference.read_bytes()).hexdigest())
            config.write_text(json.dumps(data))
            expected=singing_profile(config)
            self.assertEqual(expected['profile'],'synthetic-C')
            reference.write_bytes(b'changed reference')
            with self.assertRaisesRegex(ValueError,'resource_changed'):singing_profile(config)
            reference.write_bytes(b'synthetic voice')
            data['profile']='synthetic-D';config.write_text(json.dumps(data))
            with patch('yue_bridge.subprocess.run') as run,self.assertRaisesRegex(ValueError,'configuration_changed'):
                convert_singing_voice(config,expected,root/'source.wav',root/'out.wav')
            run.assert_not_called()

    def body(self):
        return dict(title='合成测试',mode='original',style='soft female folk',lyrics='[Verse]\n合成歌词',seed=0,duration_seconds=10)

    def test_private_recipe_binds_original_audio_and_exact_lyrics(self):
        body=self.body();body.update(mode='cover',reference_base64=base64.b64encode(b'synthetic original').decode())
        recipe=song_recipe(body)
        self.assertEqual(recipe['reference_sha256'],hashlib.sha256(b'synthetic original').hexdigest())
        self.assertEqual(recipe['lyrics'],body['lyrics'])
        self.assertEqual(recipe['seed'],0)
        self.assertNotIn('reference_base64',recipe)
        with self.assertRaises(ValueError):song_recipe(dict(body,mode='voice_convert'))

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
