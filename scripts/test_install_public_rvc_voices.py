import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import install_public_rvc_voices as installer
from voice_library import atomic_json
from singing_config import load_singing_config


class PublicRVCInstallerTests(unittest.TestCase):
    def test_download_resumes_existing_parts_and_verifies_final_hash(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / 'synthetic.pth'
            data = b'synthetic checkpoint bytes'
            target.with_name(target.name + '.part-0').write_bytes(data)
            with patch.object(installer.urllib.request, 'urlopen') as network:
                installer.fetch_asset('https://example.invalid/model', target, len(data), hashlib.sha256(data).hexdigest())
                network.assert_not_called()
            self.assertEqual(target.read_bytes(), data)
            self.assertFalse(target.with_name(target.name + '.part-0').exists())
            target.unlink()
            target.with_name(target.name + '.part-0').write_bytes(b'x' * len(data))
            with self.assertRaises(ValueError):
                installer.fetch_asset('https://example.invalid/model', target, len(data), hashlib.sha256(data).hexdigest())
            self.assertFalse(target.exists())

    def test_retains_original_and_installs_distinct_profiles_idempotently(self):
        with tempfile.TemporaryDirectory() as directory:
            workspace = Path(directory)
            root = workspace / 'public_workspace/models/voice-library'
            old = root / '曼波/weights'
            old.mkdir(parents=True)
            data = b'synthetic old model'
            sha = hashlib.sha256(data).hexdigest()
            (old / 'old.pth').write_bytes(data)
            atomic_json(old / 'singing-voice.config', {'engine': 'rvc', 'rvc_model': str(old / 'old.pth'), 'rvc_model_sha256': sha})
            (old.parent / 'README.md').write_text('old', encoding='utf-8')
            atomic_json(root / 'catalog.json', {'version': 1, 'items': [{'id': 'manbo-tlme', 'name': 'original',
                'engine': 'RVC v2', 'directory': '曼波', 'weights': [{'filename': 'old.pth',
                    'file': sha + '.pth', 'relative_path': '曼波/weights/old.pth', 'sha256': sha, 'bytes': len(data)}]}]})
            voices = []
            for role, name, identity, profile, _ in installer.VOICES:
                assets = []
                for ext in ['.pth', '.index']:
                    body = ('synthetic ' + name + ext).encode()
                    digest = hashlib.sha256(body).hexdigest()
                    cache = root / '.downloads' / (digest + ext)
                    cache.parent.mkdir(exist_ok=True)
                    cache.write_bytes(body)
                    assets.append((len(body), digest))
                voices.append((role, name, identity, profile, assets))
            with patch.object(installer, 'VOICES', voices):
                installer.install(workspace)
                installer.install(workspace)
            items = json.loads((root / 'catalog.json').read_text(encoding='utf-8'))['items']
            self.assertEqual(len(items), 4)
            self.assertEqual(len({x['profile_id'] for x in items}), 4)
            self.assertTrue((root / 'smn-曼波/weights/old.pth').is_file())
            self.assertFalse((root / '曼波/weights/old.pth').exists())
            config = load_singing_config(root / 'smn-曼波/weights/singing-voice.config')
            self.assertEqual(config['rvc_model'], str(root / 'smn-曼波/weights/old.pth'))
            self.assertEqual(items[0]['weights'][0]['file'], sha + '.pth')
            for item in items:
                manifest = workspace / 'public_workspace/manifests' / (item['profile_id'] + '.json')
                self.assertTrue(Path(json.loads(manifest.read_text(encoding='utf-8'))['configuration']).is_file())

    def test_corrupt_download_never_replaces_current_library(self):
        with tempfile.TemporaryDirectory() as directory:
            workspace = Path(directory)
            root = workspace / 'public_workspace/models/voice-library'
            root.mkdir(parents=True)
            atomic_json(root / 'catalog.json', {'version': 1, 'items': []})
            before = (root / 'catalog.json').read_bytes()
            with self.assertRaises(ValueError):
                installer.install(workspace)
            self.assertEqual((root / 'catalog.json').read_bytes(), before)


if __name__ == '__main__':
    unittest.main()
