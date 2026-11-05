"""Offline registration fixtures; no real runtime, models, downloads or sends."""
import contextlib
import hashlib
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import register_gpt_sovits as registration
import register_windows_music as windows


def write(path, content):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding='utf8', newline='\n')
    return path


def document(path, value):
    return write(path, json.dumps(value, ensure_ascii=False, indent=2))


class GPTRegistrationTests(unittest.TestCase):
    def source(self, root):
        source = root / 'source'
        for name in ('api_v2.py', 'GPT_SoVITS/text/chinese2.py',
                     'GPT_SoVITS/text/LangSegmenter/langsegmenter.py',
                     'GPT_SoVITS/text/english.py', 'GPT_SoVITS/text/g2pw/g2pw.py'):
            write(source / name, '# synthetic\nimport logging\n')
        for folder in ('chinese-roberta-wwm-ext-large', 'chinese-hubert-base'):
            document(source / 'GPT_SoVITS/pretrained_models' / folder / 'config.json', {'synthetic': True})
        subprocess.run(['git', 'init', '-q', str(source)], check=True)
        subprocess.run(['git', '-C', str(source), 'add', '.'], check=True)
        subprocess.run(['git', '-C', str(source), '-c', 'user.name=Synthetic', '-c',
                        'user.email=synthetic@example.invalid', 'commit', '-qm', 'synthetic'], check=True)
        return source

    def assert_diagnostic(self, entry, directory):
        # -I and an unrelated cwd prove no repository/import path is needed.
        result = subprocess.run([sys.executable, '-I', str(entry), '--text', 'synthetic-private-text',
                                 '--gpt', 'synthetic-private-weight'], cwd=directory,
                                text=True, capture_output=True, check=False)
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertEqual(json.loads(result.stdout), {'error': 'speech_plugin_required',
                         'inference_owner': 'plugin:speech', 'wechat_sends': 0})
        self.assertNotIn('synthetic-private', result.stdout + result.stderr)
        help_result = subprocess.run([sys.executable, '-I', str(entry), '--help'], cwd=directory,
                                     text=True, capture_output=True, check=False)
        self.assertEqual(help_result.returncode, 0, help_result.stderr)
        self.assertIn('does not run inference', help_result.stdout)
        self.assertIn('separate explicit authorization', help_result.stdout)

    def test_fresh_and_existing_resource_registration_install_only_diagnostic_stub(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = self.source(root)
            public = root / 'managed-public'
            python = public / 'runtimes/registered-python/python'
            first = registration.register(source, public, python)
            project = Path(first['root'])
            entry = Path(first['entry'])
            self.assert_diagnostic(entry, root)
            self.assertEqual(first['inference_owner'], 'plugin:speech')
            self.assertEqual(first['runtime_status'], 'registered_requires_installed_configured_speech_plugin')
            self.assertEqual(first['python'], str(python))
            self.assertEqual(first['base_models'], str(public / 'models/gpt-sovits'))
            self.assertNotIn('server_process', first['usage'])
            self.assertNotIn('music_service_start', first['execution'])
            original_source = (project / 'api_v2.py').read_bytes()
            private_model = write(public / 'models/voice-library/voice.pth', 'synthetic-kept-weight')
            before_model = private_model.read_bytes()
            # Simulate a previously copied, repo-dependent compatibility shim.
            entry.write_text('raise RuntimeError("official_speech_package_runner_required")\n', encoding='utf8')
            again = registration.register(source, public, python)
            self.assertEqual(again, first)
            self.assertEqual((project / 'api_v2.py').read_bytes(), original_source)
            self.assertEqual(private_model.read_bytes(), before_model)
            self.assertEqual(entry.read_text(encoding='utf8'), registration.PLUGIN_ENTRY)
            self.assertEqual(registration.register(source, public, python), first)
            self.assertFalse(list(public.rglob('.backups')))
            self.assert_diagnostic(entry, root)

    def test_entry_rejects_links_and_does_not_modify_link_target(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            project = root / 'project'
            project.mkdir()
            target = write(root / 'kept.py', '# keep\n')
            entry = project / 'gpt_sovits_tts.py'
            try:
                entry.symlink_to(target)
            except OSError:
                self.skipTest('symlink creation unavailable')
            with self.assertRaisesRegex(ValueError, 'linked_install_target_not_allowed'):
                registration.install_plugin_entry(project)
            self.assertEqual(target.read_text(encoding='utf8'), '# keep\n')

    def windows_fixture(self, root, overlay):
        workspace, legacy, admin, runtimes = [root / name for name in ('workspace', 'legacy', 'admin', 'program-runtimes')]
        for directory in (workspace, legacy, admin, runtimes, workspace / 'pri_workspace'):
            directory.mkdir(parents=True, exist_ok=True)
        public = workspace / 'public_workspace'
        packages = public / 'runtimes/windows-music-packages'
        for name in ('torch', 'aiohttp'):
            (packages / name).mkdir(parents=True)
        interpreter = public / 'runtimes/windows-music-python'
        interpreter.mkdir()
        pth = write(interpreter / 'python313._pth', 'python313.zip\n.\n../windows-music-packages\n../windows-music-extras\n../../projects/agent-tools\nimport site\n')
        site = write(interpreter / 'windows_music_site.py', Path(windows.__file__).with_name('windows_music_site.py').read_text(encoding='utf8'))
        custom = write(interpreter / 'sitecustomize.py', 'from windows_music_site import initialize\ninitialize()\n')
        document(interpreter / 'runtime-manifest.json', {'files': {p.name: windows.digest(p) for p in (pth, site, custom)},
                 'purpose': 'owner-authorized isolated music/GPU runtime'})
        if overlay:
            (public / 'runtimes/windows-gpt-packages').mkdir()
        write(runtimes / 'windows-music/managed_music_service.py', '# synthetic unused control')
        write(runtimes / 'windows-media/ffmpeg.exe', 'synthetic unused media')
        prefix = '/synthetic/legacy/public'
        document(legacy / 'manifests/rvc.json', {'id': 'rvc', 'root': prefix + '/projects/rvc-upstream'})
        model = write(public / 'models/singer/model.pth', 'synthetic-rvc-model')
        index = write(public / 'models/singer/model.index', 'synthetic-rvc-index')
        document(legacy / 'manifests/singing-voice.config', {'engine': 'rvc',
                 'rvc_model': prefix + '/models/singer/model.pth', 'rvc_index': prefix + '/models/singer/model.index',
                 'rvc_model_sha256': windows.digest(model), 'rvc_index_sha256': windows.digest(index)})
        project = public / 'projects/gpt-sovits'
        write(project / 'api_v2.py', '# preserve registered SDK\n')
        write(project / 'gpt_sovits_tts.py', 'raise RuntimeError("repo_path_missing")\n')
        original = {'id': 'gpt-sovits', 'root': prefix + '/projects/gpt-sovits',
                    'python': prefix + '/runtimes/old-python', 'entry': prefix + '/projects/gpt-sovits/gpt_sovits_tts.py',
                    'base_models': prefix + '/models/gpt-sovits', 'source_revision': 'synthetic-registered-revision',
                    'voice_mapping': {'registered-voice': 'unchanged-binding'}, 'usage': 'legacy-entry-inference',
                    'runtime_status': 'old-ready'}
        document(legacy / 'manifests/gpt-sovits.json', original)
        # Start from a previously imported manifest to exercise a real refresh.
        document(public / 'manifests/gpt-sovits.json', {'id': 'gpt-sovits', 'runtime_status': 'old-ready'})
        return workspace, legacy, admin, runtimes, public, original

    def test_windows_registration_updates_existing_gpt_entry_with_or_without_overlay(self):
        for overlay in (False, True):
            with self.subTest(overlay=overlay), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                workspace, legacy, admin, runtimes, public, original = self.windows_fixture(root, overlay)
                # Execute registration's real control flow; fake only external
                # resource setup/download/ACL boundaries, never GPT routing.
                with patch.object(windows, 'os', SimpleNamespace(name='nt')), \
                     patch.object(windows, 'prepare', return_value={}), \
                     patch.object(windows, 'install_gpt_nltk') as install_nltk, \
                     patch.object(windows.metadata, 'distributions', return_value=[]), \
                     patch.object(windows, 'restore_mount_inheritance') as acl, \
                     patch('urllib.request.urlopen', side_effect=AssertionError('network forbidden')), \
                     contextlib.redirect_stdout(io.StringIO()):
                    windows.register(workspace, legacy, admin, runtimes, authorize=True, repair_inheritance=False)
                    first = (public / 'manifests/gpt-sovits.json').read_bytes()
                    windows.register(workspace, legacy, admin, runtimes, authorize=True, repair_inheritance=False)
                    self.assertEqual(install_nltk.call_count, 2 if overlay else 0)
                    acl.assert_not_called()
                value = json.loads(first)
                self.assertEqual(value['source_revision'], original['source_revision'])
                self.assertEqual(value['voice_mapping'], original['voice_mapping'])
                self.assertEqual(value['base_models'], str(public / 'models/gpt-sovits'))
                self.assertEqual(value['inference_owner'], 'plugin:speech')
                self.assertEqual(value['runtime_status'], 'registered_requires_installed_configured_speech_plugin')
                self.assertNotIn('music_service_start', value['execution'])
                self.assertEqual(first, (public / 'manifests/gpt-sovits.json').read_bytes())
                self.assertEqual(value.get('packages'), str(public / 'runtimes/windows-gpt-packages') if overlay else None)
                self.assertFalse((public / 'manifests/.backups').exists())
                self.assertEqual((public / 'projects/gpt-sovits/api_v2.py').read_text(encoding='utf8'), '# preserve registered SDK\n')
                self.assertEqual((public / 'models/singer/model.pth').read_text(encoding='utf8'), 'synthetic-rvc-model')
                self.assertEqual(json.loads((legacy / 'manifests/gpt-sovits.json').read_text(encoding='utf8')), original)
                self.assert_diagnostic(Path(value['entry']), root)
                voice = json.loads((public / 'manifests/singing-voice.config').read_text(encoding='utf8'))
                self.assertEqual(voice['rvc_model_sha256'], hashlib.sha256(b'synthetic-rvc-model').hexdigest())


if __name__ == '__main__':
    unittest.main()
