import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import os

ROOT = Path(__file__).resolve().parents[1] / 'runtime'
spec = importlib.util.spec_from_file_location('native_test_subject', ROOT / 'native.py')
native = importlib.util.module_from_spec(spec)
spec.loader.exec_module(native)
spec = importlib.util.spec_from_file_location('operations_test_subject', ROOT / 'operations.py')
operations = importlib.util.module_from_spec(spec)
spec.loader.exec_module(operations)


class NativeProtocolTests(unittest.TestCase):
    def test_high_level_native_logic_has_no_local_socket_subprocess_or_raw_media(self):
        calls = []
        shot = dict(ref='blob-' + 'a' * 64, sha256='a' * 64, bytes=12 << 20, width=100, height=200)
        config = {'devices': {'phone': {'serial': 'phone', 'enabled': True, 'packages': ['com.example.app'],
                                      'actions': ['observe', 'tap', 'capture'], 'browsers': [], 'web_hosts': []}}}

        def host(request):
            self.assertEqual(request['operation'], 'device.observe')
            self.assertEqual(request['source_version'], 'f' * 64)
            self.assertEqual(request['parameters'], {})
            calls.append(request['step'])
            if request['step'] == 'profile':
                return {'scope': 'e' * 64, 'config': config}
            if request['step'] == 'foreground':
                return {'package': 'com.example.app'}
            if request['step'] == 'screenshot':
                return shot
            if request['step'] == 'ui':
                return {'xml': '<hierarchy><node text="synthetic" package="com.example.app" bounds="[0,0][10,10]"/></hierarchy>'}
            raise AssertionError(request)

        previous = Path.cwd()
        with tempfile.TemporaryDirectory() as temporary:
            try:
                os.chdir(temporary)
                with patch.object(native.logic.socket, 'create_connection', side_effect=AssertionError('socket forbidden')), patch.object(native.logic.subprocess, 'run', side_effect=AssertionError('subprocess forbidden')):
                    result = native.invoke('device.observe', {'source_version': 'f' * 64, 'request': {'device_id': 'phone'}}, host, operations.validate_request)
                self.assertEqual(result['result']['screenshot_ref']['bytes'], 12 << 20)
                self.assertNotIn('data_base64', json.dumps(result))
                self.assertEqual(result['result']['snapshot']['width'], 100)
                self.assertTrue((Path.cwd() / 'native-ledger' / ('e' * 64) / (result['result']['snapshot']['id'] + '.json')).is_file())
            finally:
                os.chdir(previous)
        self.assertEqual(calls, ['profile', 'foreground', 'screenshot', 'ui', 'foreground'])

    def test_artifact_explicit_page_is_not_clamped_to_old_two_mib(self):
        source, request = operations.validate_request('device.read_artifact', {'source_version': 'a' * 64,
            'request': {'device_id': 'phone', 'artifact_id': 'clip-' + 'b' * 32, 'request_key': 'original', 'offset': 3 << 20, 'bytes': 7 << 20}})
        self.assertEqual(request['offset'], 3 << 20)
        self.assertEqual(request['bytes'], 7 << 20)


if __name__ == '__main__':
    unittest.main()
