import json
import os
from pathlib import Path
import shutil
import socketserver
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler
from managed_music_runner import command
from managed_music_service import comfy_memory_arguments
from music_unix_client import open_request


class MusicMemoryPolicyTests(unittest.TestCase):
    def test_device_selection_is_not_forced_to_cpu_by_default(self):
        self.assertEqual(comfy_memory_arguments('auto'), [])
        self.assertEqual(comfy_memory_arguments('normal'), ['--normalvram'])
        self.assertEqual(comfy_memory_arguments('low'), ['--lowvram'])
        self.assertEqual(comfy_memory_arguments('none'), ['--novram'])
        first = comfy_memory_arguments('auto'); first.append('--cpu')
        self.assertEqual(comfy_memory_arguments('auto'), [])
        with self.assertRaisesRegex(ValueError, 'invalid_music_vram_mode'):
            comfy_memory_arguments('--gpu-only --cpu')


@unittest.skipUnless(sys.platform == "linux" and shutil.which("bwrap"), "Linux Bubblewrap integration")
class ManagedMusicTests(unittest.TestCase):
    @unittest.skipUnless(shutil.which('ffmpeg'), 'FFmpeg system runtime')
    def test_media_libraries_work_without_host_configuration(self):
        with tempfile.TemporaryDirectory() as temp:
            public=Path(temp)/'public_workspace';private=Path(temp)/'pri_workspace/account'
            public.mkdir();private.mkdir(parents=True)
            code="import subprocess;from pathlib import Path;assert not Path('/etc/passwd').exists();assert not Path('/etc/shadow').exists();subprocess.run(['ffmpeg','-version'],check=True,stdout=subprocess.DEVNULL)"
            result=subprocess.run(command(public,private,private,[str(Path('/usr/bin/python3').resolve()),'-c',code]),capture_output=True,text=True,timeout=20)
            self.assertEqual(result.returncode,0,result.stderr)
    def test_process_can_write_only_account_and_has_no_host_network(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            public = root / "public_workspace"
            private = root / "pri_workspace/account-a"
            other = root / "pri_workspace/account-b"
            public.mkdir(); private.mkdir(parents=True); other.mkdir()
            (other / "secret").write_text("synthetic")
            (public / "model").write_text("synthetic-model")
            executable = str(Path("/usr/bin/python3").resolve())
            code = "import os,socket;from pathlib import Path;assert not Path("+repr(str(other))+ ").exists();assert not os.environ.get('MANAGED_SYNTHETIC_SECRET');Path('proof').write_text('ok');s=socket.socket();s.settimeout(.2);assert s.connect_ex(('127.0.0.1',1)) != 0;print('sandbox-ok')"
            env = dict(os.environ, MANAGED_SYNTHETIC_SECRET="synthetic-no-leak")
            result = subprocess.run(command(public,private,private,[executable,"-c",code]),env=env,capture_output=True,text=True,timeout=20)
            self.assertEqual(result.returncode,0,result.stderr)
            self.assertEqual((private / "proof").read_text(),"ok")
            deny = subprocess.run(command(public,private,private,[executable,"-c","from pathlib import Path;Path("+repr(str(public / "model"))+").write_text('changed')"]),capture_output=True,text=True,timeout=20)
            self.assertNotEqual(deny.returncode,0)
            self.assertEqual((public / "model").read_text(),"synthetic-model")

    def test_links_and_control_directory_ipc_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp); public=root/'public_workspace';private=root/'pri_workspace/account';private.mkdir(parents=True);public.mkdir()
            (private/'escape').symlink_to(root,target_is_directory=True)
            with self.assertRaisesRegex(ValueError,'link_not_allowed'):
                command(public,private,private/'escape',[str(Path('/usr/bin/python3').resolve())])
            ipc=root/'ipc';ipc.mkdir(mode=0o700);(ipc/'token').write_text('synthetic')
            with self.assertRaisesRegex(ValueError,'non_socket'):
                command(public,private,private,[str(Path('/usr/bin/python3').resolve())],ipc=ipc)


@unittest.skipUnless(sys.platform == "linux", "Unix socket integration")
class UnixMusicClientTests(unittest.TestCase):
    def test_socket_http_and_destination_binding(self):
        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                data=json.dumps({'path':self.path}).encode();self.send_response(200);self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)
            def log_message(self,*unused):pass
        with tempfile.TemporaryDirectory() as temp:
            path=str(Path(temp)/'comfy.sock')
            with socketserver.UnixStreamServer(path,Handler) as server:
                worker=threading.Thread(target=server.serve_forever,daemon=True);worker.start()
                try:
                    with open_request(path,'http://127.0.0.1:8188/history/example') as reply:
                        self.assertEqual(json.load(reply),{'path':'/history/example'})
                    with self.assertRaisesRegex(ValueError,'unexpected_destination'):
                        open_request(path,'http://127.0.0.1:8081/admin')
                finally:server.shutdown();worker.join()


if __name__ == '__main__':unittest.main()
