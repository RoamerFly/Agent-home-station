"""Deterministic bridge tests: no real device, account, network or messages."""
import hashlib
import json
from pathlib import Path
import struct
import subprocess
import tempfile
import time
import unittest
from mobile_bridge import Bridge, BridgeError, ui_nodes, public_browser_url
from unittest.mock import patch

PNG = b'\x89PNG\r\n\x1a\n' + b'\x00\x00\x00\rIHDR' + struct.pack('>II', 480, 800) + b'synthetic'
XML = b'<hierarchy><node text="Hello" content-desc="" resource-id="app:id/button" class="Button" package="com.example.app" bounds="[1,2][40,50]" clickable="true" password="false"/></hierarchy>'
MP4 = b'\x00\x00\x00\x20ftyp' + b'synthetic recording fixture' * 400


class Runner:
    def __init__(self):
        self.calls = []
        self.fail_action = False
        self.png = PNG
        self.xml = XML
        self.package = 'com.example.app'
        self.capture_data = MP4

    def __call__(self, command, **kwargs):
        self.calls.append(command)
        args = command[3:]
        data = b''
        if args == ['get-state']:
            data = b'device\n'
        elif args == ['shell', 'dumpsys', 'window']:
            data = ('mCurrentFocus=Window{abc u0 ' + self.package + '/.Activity}').encode()
        elif args == ['exec-out', 'screencap', '-p']:
            data = self.png
        elif args[:2] == ['exec-out', 'cat']:
            data = self.capture_data if args[-1].endswith('.mp4') else self.xml
        elif args[:3] == ['shell', 'input', 'tap'] and self.fail_action:
            raise subprocess.TimeoutExpired(command, 15)
        return subprocess.CompletedProcess(command, 0, data, b'private stderr never forwarded')


class BridgeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.adb = self.root / 'adb'
        self.adb.write_bytes(b'synthetic')
        self.config = self.root / 'config.json'
        self.cfg = {'adb': str(self.adb), 'devices': {'lab': {'serial': 'emulator-5580', 'enabled': True,
                    'packages': ['com.example.app'], 'actions': ['observe', 'tap', 'launch', 'home', 'swipe', 'text']}}}
        self.save_config()
        self.runner = Runner()
        self.bridge = Bridge(self.config, self.root / 'state', self.runner)

    def save_config(self):
        self.config.write_text(json.dumps(self.cfg), encoding='utf8')

    def request(self, observation):
        return dict(device_id='lab', action='tap', request_key='one', observation_id=observation['id'], x=20, y=30)

    def action_calls(self):
        return [x for x in self.runner.calls if x[3:6] == ['shell', 'input', 'tap']]

    def test_recording_real_bytes_chunking_scope_and_no_replay(self):
        self.cfg['devices']['lab']['actions'].append('capture')
        self.save_config()
        obs, _ = self.bridge.observe('lab')
        request = dict(device_id='lab', action='capture', request_key='recording', observation_id=obs['id'], seconds=3)
        result = self.bridge.action(request)
        meta = result['result']['artifact']
        self.assertFalse(meta['audio'])
        self.assertFalse(meta['original_media'])
        self.assertEqual(meta['sha256'], hashlib.sha256(MP4).hexdigest())
        repeated = self.bridge.action(request)
        self.assertEqual(repeated['result']['artifact']['id'], meta['id'])
        self.assertEqual(len([c for c in self.runner.calls if c[3:5] == ['shell', 'screenrecord']]), 1)
        import base64
        actual = b''
        while len(actual) < meta['bytes']:
            chunk = self.bridge.read_artifact(dict(device_id='lab', artifact_id=meta['id'], request_key='recording', offset=len(actual)))
            self.assertLess(len(json.dumps(chunk)), 16000)
            actual += base64.b64decode(chunk['data_base64'])
        self.assertEqual(actual, MP4)
        with self.assertRaises(BridgeError):
            self.bridge.read_artifact(dict(device_id='lab', artifact_id=meta['id'], request_key='other', offset=0))
        with self.assertRaises(BridgeError):
            self.bridge.read_artifact(dict(device_id='lab', artifact_id='../config', request_key='recording', offset=0))

    def test_recording_invalid_media_never_marked_submitted(self):
        self.cfg['devices']['lab']['actions'].append('capture')
        self.save_config()
        self.runner.capture_data = b'not a video'
        obs, _ = self.bridge.observe('lab')
        result = self.bridge.action(dict(device_id='lab', action='capture', request_key='bad-recording', observation_id=obs['id'], seconds=3))
        self.assertEqual(result['state'], 'unknown')

    def test_browser_public_scope_and_shell_quoting(self):
        public = lambda *a, **k: [(2, 1, 6, '', ('93.184.216.34', 443))]
        private = lambda *a, **k: [(2, 1, 6, '', ('127.0.0.1', 443))]
        url = 'https://example.com/video?a=1&b=2'
        self.assertEqual(public_browser_url(url, ['example.com'], public), url)
        for bad in ['file:///private', 'https://user@example.com/', 'https://other.invalid/', 'https://example.com/$(id)', 'https://example.com/\ncommand']:
            with self.assertRaises(BridgeError):
                public_browser_url(bad, ['example.com'], public)
        with self.assertRaises(BridgeError):
            public_browser_url(url, ['example.com'], private)
        device = dict(packages=['com.example.app'], browsers=['com.example.app'], web_hosts=['example.com'])
        with patch('mobile_bridge.socket.getaddrinfo', public):
            command = Bridge.action_command(dict(action='open_url', package='com.example.app', url=url), {}, device)
        self.assertEqual(command[-3], "'" + url + "'")

    def test_observe_retains_real_shape_and_bounded_nodes(self):
        self.assertEqual(self.bridge.list_devices()['devices'][0]['state'], 'device')
        observation, png = self.bridge.observe('lab')
        self.assertEqual(png, PNG)
        self.assertEqual(observation['width'], 480)
        self.assertEqual(observation['screenshot_sha256'], hashlib.sha256(PNG).hexdigest())
        self.assertEqual(observation['nodes'][0]['text'], 'Hello')
        self.assertEqual((self.root / 'state' / (observation['id'] + '.png')).read_bytes(), PNG)

    def test_repeated_action_and_unknown_outcome_are_never_replayed(self):
        observation, _ = self.bridge.observe('lab')
        request = self.request(observation)
        self.runner.fail_action = True
        result = self.bridge.action(request)
        self.assertEqual(result['state'], 'unknown')
        self.runner.fail_action = False
        self.assertEqual(self.bridge.action(request)['state'], 'unknown')
        self.assertEqual(len(self.action_calls()), 1)
        request['x'] = 30
        with self.assertRaisesRegex(BridgeError, 'request_key_conflict'):
            self.bridge.action(request)
        self.assertEqual(len(self.action_calls()), 1)

    def test_changed_same_app_controls_force_reobservation(self):
        observation, _ = self.bridge.observe('lab')
        self.runner.png = PNG + b'changed'
        self.runner.xml = XML.replace(b'Hello', b'Other page')
        with self.assertRaisesRegex(BridgeError, 'page_changed_reobserve'):
            self.bridge.action(self.request(observation))
        self.assertEqual(len(self.action_calls()), 0)
        self.runner.xml = XML
        self.assertEqual(self.bridge.action(self.request(observation))['state'], 'submitted')
        self.assertEqual(len(self.action_calls()), 1)

    def test_freshness_device_scope_permissions_and_coordinates(self):
        observation, _ = self.bridge.observe('lab')
        request = self.request(observation)
        request['x'] = 9999
        with self.assertRaisesRegex(BridgeError, 'coordinate_invalid'):
            self.bridge.action(request)
        self.cfg['devices']['lab']['actions'] = ['observe']
        self.save_config()
        with self.assertRaisesRegex(BridgeError, 'not_permitted'):
            self.bridge.action(self.request(observation))
        self.cfg['devices']['lab']['actions'].append('tap')
        self.save_config()
        path = self.root / 'state' / (observation['id'] + '.json')
        observation['observed_at'] = time.time() - 1000
        path.write_text(json.dumps(observation), encoding='utf8')
        with self.assertRaisesRegex(BridgeError, 'observation_stale'):
            self.bridge.action(self.request(observation))

    def test_device_lease_applies_across_process_instances(self):
        second = Bridge(self.config, self.root / 'state', self.runner)
        with self.bridge.lease('lab'):
            with self.assertRaisesRegex(BridgeError, 'device_busy'):
                second.observe('lab')
        second.observe('lab')

    def test_forbidden_package_and_shell_injection_are_not_executed(self):
        self.runner.package = 'com.example.private'
        with self.assertRaisesRegex(BridgeError, 'foreground_not_permitted'):
            self.bridge.observe('lab')
        self.runner.package = 'com.example.app'
        observation, _ = self.bridge.observe('lab')
        request = self.request(observation)
        request.update(action='text', text='hello; cat /private')
        with self.assertRaisesRegex(BridgeError, 'text_requires_supported_ime'):
            self.bridge.action(request)
        request.update(action='launch', package='com.example.app;evil')
        with self.assertRaisesRegex(BridgeError, 'package_not_permitted'):
            self.bridge.action(request)
        with self.assertRaisesRegex(BridgeError, 'observation_required'):
            self.bridge.action(dict(request, observation_id='../../private'))

    def test_ui_entities_and_passwords(self):
        with self.assertRaisesRegex(BridgeError, 'ui_invalid'):
            ui_nodes(b'<!DOCTYPE x><x/>')
        value = ui_nodes(XML.replace(b'password="false"', b'password="true"'))
        self.assertEqual(value[0]['text'], '[password field redacted]')


if __name__ == '__main__':
    unittest.main()
