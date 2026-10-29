import base64
import json
from pathlib import Path
import tempfile
import unittest
import rvc_cover
import rvc_profiles
import rvc_recovery


class ProfileTests(unittest.TestCase):
    def test_selected_profile_is_registered_and_frozen_in_recovery(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'manifests').mkdir()
            directory = root / 'models/voice-library/manbo'
            directory.mkdir(parents=True)
            config = directory / 'voice.config'
            config.write_text('{}')
            (root / 'manifests/voice-manbo.json').write_text(json.dumps({'configuration': str(config)}))
            body = dict(title='synthetic', voice_profile_id='voice-manbo', reference_base64=base64.b64encode(b'synthetic audio').decode(), reference_seconds=30, reference_start_seconds=0, reference_pretrimmed=True)
            self.assertEqual(rvc_profiles.resolve(root, 'default', body), str(config))
            self.assertEqual(rvc_profiles.resolve(root, 'default', {}), 'default')
            jid = 'a' * 32
            rvc_recovery.persist_reference(root, jid, body)
            job = dict(engine='rvc', recipe=rvc_cover.recipe(body))
            recovered = rvc_recovery.recover_body(root, jid, job)
            self.assertEqual(recovered['voice_profile_id'], 'voice-manbo')
            self.assertEqual(rvc_cover.recipe(recovered), job['recipe'])
            for identity in ['../outside', '/tmp/config']:
                with self.assertRaises(ValueError):
                    rvc_profiles.resolve(root, 'default', {'voice_profile_id': identity})
            (root / 'manifests/voice-manbo.json').write_text(json.dumps({'configuration': str(root / 'secret.config')}))
            with self.assertRaises((ValueError, FileNotFoundError)):
                rvc_profiles.resolve(root, 'default', body)


if __name__ == '__main__':
    unittest.main()
