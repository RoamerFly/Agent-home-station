import json
import hashlib
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
from managed_music_runner import command, managed_public_root
from managed_music_service import comfy_memory_arguments, external_comfy_url, registered_comfy_command, yue_project_paths
from music_unix_client import open_request


class MusicMemoryPolicyTests(unittest.TestCase):
    def test_yue_paths_use_relocated_package_workflows(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            public = root / 'public-package'
            controls = root / 'controls'
            (public / 'runtime/comfy/ComfyUI').mkdir(parents=True)
            (public / 'runtime/comfy/ComfyUI/main.py').write_text('# synthetic', encoding='utf8')
            workflows = public / 'workflows'
            workflows.mkdir()
            for name in ('audio_yue2_text_to_music.json', 'audio_yue2_music_cover.json'):
                (workflows / name).write_text('{}', encoding='utf8')
            shipped = controls / 'workflows'
            shipped.mkdir(parents=True)
            for name in ('audio_yue2_text_to_music.json', 'audio_yue2_music_cover.json'):
                (shipped / name).write_text('wrong package', encoding='utf8')
            project, selected = yue_project_paths(public, controls)
            self.assertEqual(project, public / 'runtime')
            self.assertEqual(selected, workflows)

    def test_registered_child_uses_pinned_runtime_and_private_output(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            runtime = root / 'runtime'; runtime.mkdir()
            for name in ('python.exe', 'main.py'):
                (runtime / name).write_bytes(b'synthetic')
            (runtime / 'frontend').mkdir(); (root / 'models').mkdir()
            manifest = {'schema_version': 1, 'policy': 'station-yue-v1',
                        'files': {name: hashlib.sha256(b'synthetic').hexdigest() for name in ('python.exe', 'main.py')}}
            (runtime / 'manifest.json').write_text(json.dumps(manifest))
            config = {'schema_version': 1, 'enabled': True, 'runtime': str(runtime),
                      'python': 'python.exe', 'entry': 'main.py', 'frontend': 'frontend',
                      'manifest': 'manifest.json', 'models': str(root / 'models'), 'device': 'cpu'}
            registration = root / 'registration.json'
            registration.write_text(json.dumps(config))
            argv = registered_comfy_command(registration, 'http://127.0.0.1:6193', root / 'private')
            self.assertEqual(argv[argv.index('--port') + 1], '6193')
            self.assertEqual(argv[argv.index('--output-directory') + 1], str(root / 'private/output'))
            self.assertIn('--cpu', argv)
            (runtime / 'main.py').write_bytes(b'changed')
            with self.assertRaisesRegex(ValueError, 'digest_changed'):
                registered_comfy_command(registration, 'http://127.0.0.1:6193', root / 'private')
            config['entry'] = '../outside.py'; registration.write_text(json.dumps(config))
            with self.assertRaisesRegex(ValueError, 'path_invalid'):
                registered_comfy_command(registration, 'http://127.0.0.1:6193', root / 'private')

    def test_registered_child_accepts_windows_relative_members_and_manifest_keys(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            runtime = root / 'runtime'; runtime.mkdir()
            (runtime / 'python.exe').write_bytes(b'python')
            (runtime / 'ComfyUI').mkdir()
            (runtime / 'ComfyUI' / 'main.py').write_bytes(b'entry')
            (runtime / 'frontend').mkdir(); (root / 'models').mkdir()
            manifest = {'schema_version': 1, 'policy': 'station-yue-v1',
                        'files': {'python.exe': hashlib.sha256(b'python').hexdigest(),
                                  r'ComfyUI\main.py': hashlib.sha256(b'entry').hexdigest()}}
            (runtime / 'manifest.json').write_text(json.dumps(manifest), encoding='utf8')
            config = {'schema_version': 1, 'enabled': True, 'runtime': str(runtime),
                      'python': r'python.exe', 'entry': r'ComfyUI\main.py', 'frontend': r'frontend',
                      'manifest': r'manifest.json', 'models': str(root / 'models'), 'device': 'cpu'}
            registration = root / 'registration.json'
            registration.write_text(json.dumps(config), encoding='utf8')
            argv = registered_comfy_command(registration, 'http://127.0.0.1:6193', root / 'private')
            self.assertEqual(Path(argv[0]), runtime / 'python.exe')
            self.assertEqual(Path(argv[3]), runtime / 'ComfyUI' / 'main.py')

    def test_registered_public_tool_root_is_accepted(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp) / 'rvc-换声'
            (root / 'runtime').mkdir(parents=True)
            (root / 'plugin.json').write_text(json.dumps({'id': 'rvc'}), encoding='utf8')
            self.assertEqual(managed_public_root(root), root.resolve())
            (root / 'plugin.json').write_text('{}', encoding='utf8')
            with self.assertRaisesRegex(ValueError, 'manifest_invalid'):
                managed_public_root(root)

    def test_device_selection_is_not_forced_to_cpu_by_default(self):
        self.assertEqual(comfy_memory_arguments('auto'), [])
        self.assertEqual(comfy_memory_arguments('normal'), ['--normalvram'])
        self.assertEqual(comfy_memory_arguments('low'), ['--lowvram'])
        self.assertEqual(comfy_memory_arguments('none'), ['--novram'])
        first = comfy_memory_arguments('auto'); first.append('--cpu')
        self.assertEqual(comfy_memory_arguments('auto'), [])
        with self.assertRaisesRegex(ValueError, 'invalid_music_vram_mode'):
            comfy_memory_arguments('--gpu-only --cpu')

    def test_external_comfy_url_is_registered_loopback_only(self):
        self.assertEqual(external_comfy_url(None), None)
        self.assertEqual(external_comfy_url('http://127.0.0.1:6191'), 'http://127.0.0.1:6191')
        for value in ('http://localhost:6191', 'http://192.168.1.2:6191',
                      'https://127.0.0.1:6191', 'http://127.0.0.1:80',
                      'http://127.0.0.1:6191/path'):
            with self.assertRaises(ValueError):
                external_comfy_url(value)


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
