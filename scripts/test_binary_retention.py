import hashlib
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from binary_retention import plan_retention, apply_retention


class RetentionTests(unittest.TestCase):
    def make(self, root, content, age, program='wechat-admin'):
        data=b'\x7fELF'+content.encode()
        path=root/(program+'.previous-'+hashlib.sha256(data).hexdigest()[:12])
        path.write_bytes(data);os.utime(path,ns=(age,age));return path

    def test_only_verified_old_binaries_and_all_data_survive(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp).resolve();(root/'wechat-admin').write_bytes(b'\x7fELFcurrent')
            first=self.make(root,'old',100);latest=self.make(root,'new',200)
            media=root/'song.wav';media.write_bytes(b'original media')
            unknown=root/'wechat-admin.previous-deadbeef1234';unknown.write_bytes(b'\x7fELFwrong hash')
            plan=plan_retention(root,1)
            self.assertTrue(first.exists(),'preview deleted a file')
            self.assertEqual([x['name']for x in plan['remove']],[first.name])
            self.assertIn(unknown.name,plan['skipped'])
            result=apply_retention(plan,plan['plan_hash'])
            self.assertEqual(result['removed'],[first.name]);self.assertTrue(latest.exists())
            self.assertTrue(media.exists());self.assertTrue(unknown.exists());self.assertTrue((root/'wechat-admin').exists())

    def test_stale_plan_fails_before_any_deletion(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp).resolve();(root/'wx').write_bytes(b'\x7fELFcurrent')
            old=self.make(root,'old',100,'wx');new=self.make(root,'new',200,'wx')
            plan=plan_retention(root,1);new.write_bytes(b'changed')
            with self.assertRaises(ValueError):apply_retention(plan,plan['plan_hash'])
            self.assertTrue(old.exists())

    def test_active_hash_copy_is_kept_and_links_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp).resolve();(root/'wx').write_bytes(b'\x7fELFcurrent')
            same=self.make(root,'current',100,'wx');self.make(root,'new',200,'wx')
            plan=plan_retention(root,1);self.assertEqual(plan['remove'],[])
            self.assertIn(same.name,[x['name']for x in plan['retained']])
            with self.assertRaises(ValueError):apply_retention(plan,'wrong')
            with self.assertRaises(ValueError):plan_retention(root,0)

    def test_mapped_old_executable_is_protected(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp).resolve();(root/'wx').write_bytes(b'\x7fELFcurrent')
            running=self.make(root,'mapped-old',100,'wx');self.make(root,'new',200,'wx')
            with patch('binary_retention.running_hashes',return_value={hashlib.sha256(running.read_bytes()).hexdigest()}):
                self.assertEqual(plan_retention(root,1)['remove'],[])

    @unittest.skipIf(os.name=='nt','Windows symlink creation may require administrator')
    def test_symlink_backup_and_noncanonical_root_are_never_targets(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp).resolve();(root/'wx').write_bytes(b'\x7fELFcurrent')
            external=root/'external';external.write_bytes(b'\x7fELFexternal')
            name='wx.previous-'+hashlib.sha256(external.read_bytes()).hexdigest()[:12]
            (root/name).symlink_to(external)
            plan=plan_retention(root,1);self.assertEqual(plan['remove'],[]);self.assertIn(name,plan['skipped'])
            link=root/'linked-root';link.symlink_to(root,target_is_directory=True)
            with self.assertRaises(ValueError):plan_retention(link,1)


if __name__=='__main__':unittest.main()
