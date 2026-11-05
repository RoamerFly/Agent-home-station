import hashlib
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import tempfile
import unittest

from managed_music_ipc import AccountIPC
from managed_music_runner import command


@unittest.skipUnless(sys.platform == "linux", "Linux account IPC lease")
class MusicIPCTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.public = self.root / "public_workspace"
        self.public.mkdir()
        self.private = self.root / "pri_workspace" / hashlib.sha256(b"one").hexdigest()
        self.private.mkdir(parents=True)
        self.base = self.root / "ipc"

    def lease(self, private=None):
        lease = AccountIPC(self.base, self.public, private or self.private)
        self.addCleanup(lease.close)
        return lease

    def test_two_accounts_and_duplicate_process_preserve_live_socket(self):
        first = self.lease()
        other = self.private.parent / hashlib.sha256(b"two").hexdigest()
        other.mkdir()
        second = self.lease(other)
        self.assertNotEqual(first.directory, second.directory)
        with socket.socket(socket.AF_UNIX) as a, socket.socket(socket.AF_UNIX) as b:
            a.bind(str(first.socket)); a.listen()
            b.bind(str(second.socket)); b.listen()
            ino = first.socket.stat().st_ino
            code = "from managed_music_ipc import AccountIPC;import sys;AccountIPC(*sys.argv[1:])"
            env = dict(os.environ, PYTHONPATH=str(Path(__file__).parent.resolve()))
            result = subprocess.run([sys.executable, "-c", code, str(self.base), str(self.public), str(self.private)],
                                    env=env, capture_output=True, text=True, timeout=5)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("music_account_service_already_running", result.stderr)
            self.assertEqual(first.socket.stat().st_ino, ino)
            for path in (first.socket, second.socket):
                with socket.socket(socket.AF_UNIX) as probe:
                    probe.connect(str(path))

    def test_live_orphan_not_deleted_and_stale_socket_can_restart(self):
        first = self.lease()
        with socket.socket(socket.AF_UNIX) as server:
            server.bind(str(first.socket)); server.listen()
            ino = first.socket.stat().st_ino
            first.close()
            with self.assertRaisesRegex(RuntimeError, "backend_still_running"):
                self.lease()
            self.assertEqual(first.socket.stat().st_ino, ino)
        restarted = self.lease()
        self.assertFalse(restarted.socket.exists())

    def test_full_identity_checked_even_with_matching_prefix(self):
        first = self.lease()
        first.close()
        other = self.private.parent / (self.private.name[:24] + "f" * 40)
        other.mkdir()
        with self.assertRaisesRegex(ValueError, "identity_mismatch"):
            self.lease(other)

    def test_reject_links_files_unsafe_control_and_workspace_paths(self):
        first = self.lease()
        first.close()
        first.socket.symlink_to(self.root / "absent")
        with self.assertRaisesRegex(ValueError, "unexpected_music_ipc_file"):
            self.lease()
        first.socket.unlink()
        first.socket.write_text("not a socket")
        with self.assertRaisesRegex(ValueError, "unexpected_music_ipc_file"):
            self.lease()
        for path in (self.public / "ipc", self.private / "ipc", self.private.parent / "ipc"):
            with self.assertRaisesRegex(ValueError, "inside_agent_workspace"):
                AccountIPC(path, self.public, self.private)
            self.assertFalse(path.exists())
        unsafe = self.root / "unsafe"
        unsafe.mkdir(mode=0o755)
        with self.assertRaisesRegex(ValueError, "private_directory"):
            AccountIPC(unsafe, self.public, self.private)
        self.assertEqual(unsafe.stat().st_mode & 0o777, 0o755)
        link = self.root / "link"
        link.symlink_to(self.base, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, "link_not_allowed"):
            AccountIPC(link, self.public, self.private)

    def test_socket_path_length_and_lock_does_not_survive_exec(self):
        with self.assertRaisesRegex(ValueError, "path_too_long"):
            AccountIPC(self.root / ("long" * 24), self.public, self.private)
        first = self.lease()
        self.assertFalse(os.get_inheritable(first.fd))
        child = subprocess.Popen([sys.executable, "-c", "import time;time.sleep(10)"], close_fds=False)
        try:
            first.close()
            self.lease()
        finally:
            child.terminate(); child.wait(timeout=5)

    @unittest.skipUnless(shutil.which("bwrap"), "Bubblewrap IPC mount")
    def test_inference_sees_only_socket_leaf_not_lease_or_other_account(self):
        first = self.lease()
        other = self.private.parent / hashlib.sha256(b"two").hexdigest()
        other.mkdir()
        second = self.lease(other)
        code = ("from pathlib import Path;"
                "assert list(Path('/run/managed-music').iterdir()) == [];"
                f"assert not Path({str(first.directory.parent)!r}).exists();"
                f"assert not Path({str(second.directory)!r}).exists()")
        result = subprocess.run(command(self.public, self.private, self.private,
                                        [str(Path('/usr/bin/python3').resolve()), "-c", code], ipc=first.directory),
                                capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
