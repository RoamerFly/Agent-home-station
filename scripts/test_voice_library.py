import json
from pathlib import Path
import tempfile
import unittest
from voice_library import import_history, store_file, atomic_json


class VoiceLibraryTests(unittest.TestCase):
    def test_pairing_versions_epochs_and_reference_are_retained(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history, library = root / 'history', root / 'library'
            for folder in ['GPT_weights_v2', 'SoVITS_weights_v2', 'SoVITS_weights', 'A_Example']:
                (history / folder).mkdir(parents=True)
            for folder, name in [('GPT_weights_v2', 'mom-e5.ckpt'), ('GPT_weights_v2', 'mom-e15.ckpt'),
                                 ('SoVITS_weights_v2', 'mom_e8_s272.pth'), ('SoVITS_weights', 'bb_e8_s280.pth')]:
                (history / folder / name).write_bytes(name.encode())
            for ext in ['.wav', '.txt']:
                (history / 'A_Example' / ('mom_example' + ext)).write_bytes(b'synthetic reference')
            items = import_history(history, library)
            mom = next(x for x in items if x['name'] == 'mom')
            self.assertTrue(mom['paired'])
            self.assertEqual(len(mom['weights']), 3)
            self.assertEqual(len(mom['references']), 2)
            newest = next(x for x in mom['weights'] if x['epoch'] == 15)
            self.assertEqual(mom['recommended_weights']['gpt'], newest['file'])
            bb = next(x for x in items if x['name'] == 'bb')
            self.assertFalse(bb['paired'])
            self.assertEqual(bb['weights'][0]['version'], 'v1')
            count = len(list((library / 'files').iterdir()))
            self.assertEqual(items, import_history(history, library))
            self.assertEqual(count, len(list((library / 'files').iterdir())))

    def test_objects_are_verified_and_catalog_is_atomic(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / 'source.wav'
            source.write_bytes(b'synthetic')
            item = store_file(source, root / 'files')
            self.assertEqual(source.read_bytes(), b'synthetic')
            (root / 'files' / item['file']).write_bytes(b'corrupted')
            with self.assertRaises(ValueError):
                store_file(source, root / 'files')
            atomic_json(root / 'catalog.json', {'items': []})
            self.assertEqual(json.loads((root / 'catalog.json').read_text()), {'items': []})
            self.assertFalse(list(root.glob('.pending-*')))


if __name__ == '__main__':
    unittest.main()
