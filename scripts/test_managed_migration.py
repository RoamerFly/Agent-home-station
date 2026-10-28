import tempfile
import unittest
from pathlib import Path
from migrate_managed_workspaces import migrate

class MigrationTests(unittest.TestCase):
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
