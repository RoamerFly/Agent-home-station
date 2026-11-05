import tempfile
import unittest
from pathlib import Path
from migrate_managed_workspaces import migrate

class MigrationTests(unittest.TestCase):
    def test_root_weight_exclusion_keeps_nested_model_source(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d)/'bot';root.mkdir();source=Path(d)/'source'
            (source/'models').mkdir(parents=True);(source/'models/weights.bin').write_bytes(b'synthetic')
            (source/'comfy/ldm/models').mkdir(parents=True);(source/'comfy/ldm/models/autoencoder.py').write_text('# synthetic source')
            (source/'comfy_api/input').mkdir(parents=True);(source/'comfy_api/input/curve.py').write_text('# synthetic API')
            (source/'input').mkdir();(source/'input/private.wav').write_bytes(b'synthetic private media')
            plan={'root':str(root),'copies':[{'source':str(source),'target':'public_workspace/projects/yue','exclude_paths':['models']}]}
            migrate(plan,True)
            target=root/'public_workspace/projects/yue'
            self.assertFalse((target/'models').exists())
            self.assertEqual((target/'comfy/ldm/models/autoencoder.py').read_text(),'# synthetic source')
            self.assertTrue((target/'comfy_api/input/curve.py').is_file())
            self.assertFalse((target/'input').exists())
            plan['copies'][0]['exclude_paths']=['../outside']
            with self.assertRaisesRegex(ValueError,'invalid_exclude_path'):migrate(plan,True)
    def test_copy_verify_resume_and_conflict_preserves_original(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d)/'bot';root.mkdir();source=Path(d)/'old';source.mkdir()
            (source/'model.bin').write_bytes(b'synthetic-model')
            plan={'root':str(root),'copies':[{'source':str(source),'target':'public_workspace/projects/synthetic'}]}
            a=migrate(plan,True);b=migrate(plan,True)
            self.assertEqual(a,b);self.assertEqual(a['originals_deleted'],0)
            dest=root/'public_workspace/projects/synthetic/model.bin';dest.write_bytes(b'changed')
            with self.assertRaisesRegex(ValueError,'destination_conflict'):migrate(plan,True)
            self.assertEqual(dest.read_bytes(),b'changed');self.assertEqual((source/'model.bin').read_bytes(),b'synthetic-model')
    def test_target_escape_rejected(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d)/'bot';root.mkdir();source=Path(d)/'file';source.write_text('synthetic')
            with self.assertRaisesRegex(ValueError,'target_outside'):migrate({'root':str(root),'copies':[{'source':str(source),'target':'../escape'}]},True)
    def test_target_link_rejected(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d)/'bot';root.mkdir();outside=Path(d)/'outside';outside.mkdir();source=Path(d)/'file';source.write_text('synthetic')
            link=root/'public_workspace';
            try:link.symlink_to(outside,target_is_directory=True)
            except OSError:self.skipTest('symlinks unavailable')
            with self.assertRaisesRegex(ValueError,'destination_link'):migrate({'root':str(root),'copies':[{'source':str(source),'target':'public_workspace/escape'}]},True)
if __name__=='__main__':unittest.main()
