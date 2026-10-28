"""Synthetic migration conflicts: never overwrite a concurrently created file."""
import os
import errno
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import subprocess
import threading
import ctypes
import stat
from migration_io import Directory

from migrate_managed_workspaces import migrate


class MigrationIntegrityTests(unittest.TestCase):
    def exclusive_rename_available(self,directory):
        if os.name=='nt':return True
        # Probe the filesystem/kernel contract directly, independently of the
        # migration wrapper. DrvFS/9p can reject RENAME_NOREPLACE with EINVAL.
        call=getattr(ctypes.CDLL(None,use_errno=True),'renameat2',None)
        if call is None:return False
        call.argtypes=[ctypes.c_int,ctypes.c_char_p,ctypes.c_int,ctypes.c_char_p,ctypes.c_uint];call.restype=ctypes.c_int
        root=Path(directory);left=root/'.rename-probe-from';right=root/'.rename-probe-to';left.write_bytes(b'synthetic probe')
        fd=os.open(root,os.O_RDONLY|os.O_DIRECTORY)
        try:
            if call(fd,os.fsencode(left.name),fd,os.fsencode(right.name),1)==0:return True
            code=ctypes.get_errno()
            if code in (errno.EINVAL,errno.ENOSYS,errno.EOPNOTSUPP):return False
            raise OSError(code,os.strerror(code))
        finally:os.close(fd);left.unlink(missing_ok=True);right.unlink(missing_ok=True)

    def pipe(self,path):
        try:os.mkfifo(path)
        except OSError as exc:
            if exc.errno in (errno.EOPNOTSUPP,errno.ENOSYS):self.skipTest('filesystem cannot create a FIFO; native Linux tests cover nonblocking open')
            raise

    def fixture(self, directory):
        root=Path(directory)/'bot';root.mkdir()
        source=Path(directory)/'source.bin';source.write_bytes(b'synthetic registered original')
        target=root/'public_workspace/projects/sample.bin'
        plan={'root':str(root),'copies':[{'source':str(source),'target':'public_workspace/projects/sample.bin'}]}
        return root,source,target,plan

    def test_concurrent_different_destination_is_preserved(self):
        with tempfile.TemporaryDirectory() as temporary:
            root,source,target,plan=self.fixture(temporary)
            replace,link=Path.replace,os.link
            def compete_replace(pending,destination):
                if pending.name.startswith('.migration-') and Path(destination).name==target.name:
                    target.write_bytes(b'concurrent user edit')
                return replace(pending,destination)
            def compete_link(pending,destination,*args,**kwargs):
                if Path(pending).name.startswith('.migration-') and Path(destination).name==target.name:
                    target.write_bytes(b'concurrent user edit')
                return link(pending,destination,*args,**kwargs)
            with patch.object(Path,'replace',compete_replace),patch('os.link',side_effect=compete_link):
                with self.assertRaisesRegex(ValueError,'destination_conflict'):migrate(plan,True)
            self.assertEqual(target.read_bytes(),b'concurrent user edit')
            self.assertEqual(source.read_bytes(),b'synthetic registered original')
            self.assertEqual(list(target.parent.glob('.migration-*')),[])

    @unittest.skipIf(os.name=='nt','symlink privileges are not required by this test suite')
    def test_dangling_module_marker_cannot_create_an_outside_file(self):
        with tempfile.TemporaryDirectory() as temporary:
            root,source,target,plan=self.fixture(temporary)
            public=root/'public_workspace';public.mkdir()
            outside=Path(temporary)/'outside-created.mod'
            (public/'go.mod').symlink_to(outside)
            with self.assertRaises(ValueError):migrate(plan,True)
            self.assertFalse(outside.exists())
            self.assertFalse(target.exists())

    def test_matching_concurrent_destination_is_reused_without_replacing_inode(self):
        with tempfile.TemporaryDirectory() as temporary:
            root,source,target,plan=self.fixture(temporary)
            link=os.link;identity=[]
            def competing(pending,destination,*args,**kwargs):
                if Path(pending).name.startswith('.migration-') and Path(destination).name==target.name:
                    target.write_bytes(source.read_bytes());identity.append(target.stat().st_ino)
                return link(pending,destination,*args,**kwargs)
            with patch('os.link',side_effect=competing):result=migrate(plan,True)
            self.assertEqual(target.stat().st_ino,identity[0])
            self.assertEqual(result['bytes'],source.stat().st_size)
            self.assertEqual(list(target.parent.glob('.migration-*')),[])

    def test_unavailable_hardlinks_do_not_fall_back_to_replace(self):
        with tempfile.TemporaryDirectory() as temporary:
            root,source,target,plan=self.fixture(temporary)
            with patch('os.link',side_effect=OSError('synthetic link failure')):
                with self.assertRaisesRegex(ValueError,'atomic_publish_unavailable'):migrate(plan,True)
            self.assertFalse(target.exists());self.assertTrue(source.is_file())
            self.assertEqual(list(root.rglob('.migration-*')),[])

    def test_unsupported_hardlink_uses_atomic_rename_or_reports_real_boundary(self):
        with tempfile.TemporaryDirectory() as temporary:
            root,source,target,plan=self.fixture(temporary)
            supported=self.exclusive_rename_available(temporary)
            with patch('os.link',side_effect=OSError(errno.EOPNOTSUPP,'synthetic unsupported link')),patch('os.replace',side_effect=AssertionError('overwriting rename used')):
                if supported:migrate(plan,True)
                else:
                    with self.assertRaisesRegex(ValueError,'atomic_publish_unavailable'):migrate(plan,True)
            if supported:self.assertEqual(target.read_bytes(),source.read_bytes())
            else:self.assertFalse(target.exists())
            self.assertEqual(list(root.rglob('.migration-*')),[])

    def test_atomic_rename_fallback_still_preserves_concurrent_destination(self):
        with tempfile.TemporaryDirectory() as temporary:
            root,source,target,plan=self.fixture(temporary)
            supported=self.exclusive_rename_available(temporary)
            # Test target publication rather than stop at a module marker when
            # this filesystem lacks the forced fallback operation.
            for name in ('public_workspace','pri_workspace'):
                base=root/name;base.mkdir();(base/'go.mod').write_text('module synthetic.existing\n')
            original=Directory.rename_no_replace
            def compete(directory,pending,name):
                if name==target.name:target.write_bytes(b'concurrent user edit')
                return original(directory,pending,name)
            with patch('os.link',side_effect=OSError(errno.EOPNOTSUPP,'synthetic unsupported link')),patch.object(Directory,'rename_no_replace',compete):
                with self.assertRaisesRegex(ValueError,'destination_conflict' if supported else '(destination_conflict|atomic_publish_unavailable)'):migrate(plan,True)
            self.assertEqual(target.read_bytes(),b'concurrent user edit')
            self.assertEqual(source.read_bytes(),b'synthetic registered original')
            self.assertEqual(list(root.rglob('.migration-*')),[])

    def test_concurrent_custom_module_is_preserved(self):
        with tempfile.TemporaryDirectory() as temporary:
            root,source,target,plan=self.fixture(temporary)
            link=os.link;custom=b'module synthetic.custom/project\n'
            marker=root/'public_workspace/go.mod'
            def competing(pending,destination,*args,**kwargs):
                if Path(destination).name=='go.mod' and not marker.exists():marker.write_bytes(custom)
                return link(pending,destination,*args,**kwargs)
            with patch('os.link',side_effect=competing):migrate(plan,True)
            self.assertEqual(marker.read_bytes(),custom)
            self.assertEqual(target.read_bytes(),source.read_bytes())

    def test_parent_substitution_never_writes_to_an_outside_directory(self):
        with tempfile.TemporaryDirectory() as temporary:
            root,source,target,plan=self.fixture(temporary)
            outside=Path(temporary)/'outside';outside.mkdir()
            moved=[];method=Directory.temporary
            def swap(directory):
                if directory.path==target.parent and not moved:
                    try:target.parent.rename(root/'public_workspace/original-projects')
                    except PermissionError:
                        moved.append(False)
                    else:
                        moved.append(True);target.parent.symlink_to(outside,target_is_directory=True)
                return method(directory)
            failed=False
            with patch.object(Directory,'temporary',swap):
                try:migrate(plan,True)
                except (OSError,ValueError):failed=True
            self.assertEqual(list(outside.iterdir()),[])
            self.assertTrue(source.is_file())
            if os.name=='nt':
                self.assertEqual(moved,[False],'Windows ancestor was not locked against rename')
                self.assertFalse(failed);self.assertEqual(target.read_bytes(),source.read_bytes())
            else:self.assertTrue(failed)
            self.assertEqual(list(root.rglob('.migration-*')),[])

    @unittest.skipIf(os.name=='nt','POSIX file replacement without symlink privileges')
    def test_source_leaf_replacement_is_not_followed(self):
        with tempfile.TemporaryDirectory() as temporary:
            root,source,target,plan=self.fixture(temporary)
            outside=Path(temporary)/'private.txt';outside.write_bytes(b'synthetic private canary')
            original=Directory.open_regular;swapped=[]
            def swap(directory,name):
                if directory.path==source.parent and name==source.name and not swapped:
                    source.rename(source.with_suffix('.saved'));source.symlink_to(outside);swapped.append(True)
                return original(directory,name)
            with patch.object(Directory,'open_regular',swap):
                with self.assertRaises(ValueError):migrate(plan,True)
            self.assertFalse(target.exists());self.assertEqual(outside.read_bytes(),b'synthetic private canary')

    @unittest.skipUnless(os.name=='nt','real Windows Junction without symlink privileges')
    def test_source_junction_is_rejected_without_visible_console(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)/'bot';root.mkdir();source=Path(temporary)/'source';source.mkdir()
            outside=Path(temporary)/'outside';outside.mkdir();(outside/'private.txt').write_bytes(b'synthetic')
            subprocess.run(['cmd.exe','/d','/c','mklink','/J',str(source/'alias'),str(outside)],check=True,
                           capture_output=True,creationflags=0x08000000)
            plan={'root':str(root),'copies':[{'source':str(source),'target':'public_workspace/project'}]}
            with self.assertRaisesRegex(ValueError,'source_link'):migrate(plan,True)
            self.assertFalse((root/'public_workspace/project/private.txt').exists())

    @unittest.skipIf(os.name=='nt','POSIX special file')
    def test_pipe_source_is_rejected_in_preview_and_apply(self):
        with tempfile.TemporaryDirectory() as temporary:
            root,source,target,plan=self.fixture(temporary);source.unlink();self.pipe(source)
            for apply in (False,True):
                with self.assertRaisesRegex(ValueError,'source_not_regular'):migrate(plan,apply)
            self.assertFalse(target.exists())

    @unittest.skipIf(os.name=='nt','POSIX source-root symlink')
    def test_registered_source_root_link_is_not_resolved_to_private_data(self):
        with tempfile.TemporaryDirectory() as temporary:
            root,source,target,plan=self.fixture(temporary)
            outside=Path(temporary)/'outside.txt';outside.write_bytes(b'synthetic outside data')
            source.unlink();source.symlink_to(outside)
            for apply in (False,True):
                with self.assertRaisesRegex(ValueError,'source_root_link'):migrate(plan,apply)
            self.assertFalse(target.exists())

    def test_changed_source_is_not_published_or_reverted(self):
        with tempfile.TemporaryDirectory() as temporary:
            root,source,target,plan=self.fixture(temporary)
            method=Directory.temporary
            def modify(directory):
                if directory.path==target.parent:source.write_bytes(b'new user version')
                return method(directory)
            with patch.object(Directory,'temporary',modify):
                with self.assertRaisesRegex(ValueError,'copy_integrity_failed'):migrate(plan,True)
            self.assertEqual(source.read_bytes(),b'new user version')
            self.assertFalse(target.exists());self.assertEqual(list(root.rglob('.migration-*')),[])

    def test_partial_conflict_resumes_without_replacing_verified_files(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)/'bot';root.mkdir();source=Path(temporary)/'source';source.mkdir()
            (source/'a.bin').write_bytes(b'a');(source/'b.bin').write_bytes(b'b')
            target=root/'public_workspace/projects/sample';target.mkdir(parents=True)
            (target/'b.bin').write_bytes(b'user b')
            private=root/'pri_workspace/other-account/keep.txt';private.parent.mkdir(parents=True);private.write_bytes(b'private synthetic')
            plan={'root':str(root),'copies':[{'source':str(source),'target':'public_workspace/projects/sample'}]}
            with self.assertRaisesRegex(ValueError,'destination_conflict'):migrate(plan,True)
            inode=(target/'a.bin').stat().st_ino
            self.assertEqual((target/'b.bin').read_bytes(),b'user b')
            (source/'b.bin').write_bytes(b'user b')
            self.assertEqual(migrate(plan,True)['files'],2)
            self.assertEqual((target/'a.bin').stat().st_ino,inode)
            self.assertEqual(private.read_bytes(),b'private synthetic')

    @unittest.skipIf(os.name=='nt','POSIX nonblocking open')
    def test_pipe_substituted_at_open_does_not_wait_for_a_writer(self):
        with tempfile.TemporaryDirectory() as temporary:
            pipe=Path(temporary)/'pipe';self.pipe(pipe)
            done=threading.Event();errors=[]
            with Directory(Path(temporary)) as directory:
                def read():
                    try:
                        with directory.open_regular('pipe'):pass
                    except BaseException as exc:errors.append(exc)
                    finally:done.set()
                worker=threading.Thread(target=read,daemon=True);worker.start()
                if not done.wait(1):
                    try:
                        fd=os.open(pipe,os.O_WRONLY|os.O_NONBLOCK);os.close(fd)
                    except OSError:pass
                    done.wait(1)
                    self.fail('special-file open blocked before type validation')
                self.assertEqual(len(errors),1)
                self.assertIsInstance(errors[0],ValueError)

    @unittest.skipIf(os.name=='nt','POSIX executable mode')
    def test_copied_program_retains_execute_without_special_permission_bits(self):
        with tempfile.TemporaryDirectory() as temporary:
            root,source,target,plan=self.fixture(temporary)
            source.write_bytes(b'#!/bin/sh\nprintf managed-migration-ok\n');source.chmod(0o4755)
            if stat.S_IMODE(source.stat().st_mode)!=0o4755:self.skipTest('filesystem does not preserve POSIX mode bits')
            migrate(plan,True)
            self.assertEqual(stat.S_IMODE(target.stat().st_mode),0o700)
            self.assertEqual(subprocess.run([str(target)],check=True,capture_output=True,timeout=5).stdout,b'managed-migration-ok')


if __name__=='__main__':unittest.main()
