"""Synthetic direct conversion checks; no GPU, real song or WeChat sends."""
import base64
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import rvc_cover


class DirectCoverTests(unittest.TestCase):
    def body(self):
        return {'title':'Synthetic song','reference_base64':base64.b64encode(b'synthetic vocal recording').decode(),'reference_seconds':140,'reference_start_seconds':0,'reference_pretrimmed':True}

    def test_original_audio_is_used_without_lyrics_generation_or_yue(self):
        body=self.body();calls=[]
        def conversion(config,profile,source,output):
            calls.append((config,profile))
            self.assertEqual(source.read_bytes(),b'synthetic vocal recording')
            output.write_bytes(b'RIFF synthetic converted audio')
        with tempfile.TemporaryDirectory() as tmp,patch('rvc_cover.subprocess.check_output',return_value=b'140.02'):
            result=rvc_cover.convert(body,Path(tmp),'a'*32,'profile.json',{'engine':'rvc'},conversion)
        self.assertEqual(len(calls),1)
        self.assertEqual(result['requested_seconds'],140)
        self.assertEqual(result['duration_seconds'],140.02)
        self.assertEqual(result['quality']['engine'],'rvc')
        self.assertFalse(result['quality']['timbre_listening_verified'])
        recipe=rvc_cover.recipe(body)
        self.assertEqual(recipe['melody_source'],'original_audio')
        self.assertEqual(recipe['lyrics_source'],'original_audio')
        self.assertNotIn('reference_base64',recipe)
        self.assertNotIn('sampling',recipe)

    def test_mismatched_duration_is_not_marked_completed(self):
        def conversion(config,profile,source,output):output.write_bytes(b'synthetic')
        with tempfile.TemporaryDirectory() as tmp,patch('rvc_cover.subprocess.check_output',return_value=b'25'):
            with self.assertRaisesRegex(ValueError,'rvc_duration_mismatch'):
                rvc_cover.convert(self.body(),Path(tmp),'a'*32,'profile.json',{'engine':'rvc'},conversion)

    def test_invalid_source_and_wrong_engine_reject_before_conversion(self):
        body=self.body()
        for overrides in ({'reference_seconds':0},{'reference_seconds':True},{'reference_seconds':601},{'reference_seconds':float('nan')},{'reference_start_seconds':-1},{'reference_base64':'invalid'},{'reference_pretrimmed':False}):
            with self.subTest(overrides=overrides),self.assertRaises(ValueError):rvc_cover.recipe(dict(body,**overrides))
        with self.assertRaisesRegex(ValueError,'rvc_profile_required'):
            rvc_cover.convert(body,Path('unused'),'synthetic','profile.json',{'engine':'seed-vc'},None)


if __name__=='__main__':unittest.main()
