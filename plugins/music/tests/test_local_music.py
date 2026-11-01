"""Local bridge protocol fixtures; never open sockets, models or media files."""
import copy
import importlib.util
import io
import json
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('local_music_main', ROOT / 'runtime/main.py')
main = importlib.util.module_from_spec(spec)
spec.loader.exec_module(main)
local = main.local_music
BODY = {'ref': 'request_0', 'sha256': 'a' * 64, 'bytes': 1234}


def reply(status=200, payload=None, mime='application/json'):
    out = {'status': status, 'mime': mime, 'body_ref': {'ref': 'reply_0', 'sha256': 'b' * 64, 'bytes': 987}}
    if payload is not None:
        out['json'] = payload
    return out


class LocalMusicTests(unittest.TestCase):
    def request(self, method='GET', path='/health'):
        out = {'method': method, 'path': path}
        if method == 'POST':
            out['body_ref'] = copy.deepcopy(BODY)
        if path in ('/jobs', '/rvc-jobs'):
            out['idempotency_key'] = 'same-original-operation'
        return out

    def test_exact_start_status_audio_cancel_resume_routes_and_byte_refs(self):
        routes = [('GET', path) for path in ('/health', '/rvc-health', '/parameters', '/sampling-options',
                  '/jobs/original', '/jobs/original/audio')]
        routes += [('POST', path) for path in ('/jobs', '/rvc-jobs', '/jobs/original/cancel', '/jobs/original/resume')]
        for method, path in routes:
            args = self.request(method, path)
            observed = reply(200, {'id': 'original', 'state': 'running'})
            if path.endswith('/audio'):
                observed = reply(200, mime='audio/wav')
            calls = []
            out = local.bridge_request(args, lambda request: calls.append(copy.deepcopy(request)) or observed)
            with self.subTest(method=method, path=path):
                self.assertEqual(calls, [args])
                self.assertEqual(out, observed)
                self.assertNotIn('normalized_error', out)
                self.assertNotIn('lyrics', json.dumps(calls))
                self.assertNotIn('base64', json.dumps(calls))
                self.assertNotIn('token', json.dumps(calls))
        # Empty cancel has no recipe; it cannot create another original job.
        local.validate_request({'method': 'POST', 'path': '/jobs/original/cancel'})

    def test_wrong_paths_methods_or_raw_bodies_never_call_host(self):
        invalid = [self.request('GET', path) for path in ('https://localhost/jobs', '//jobs', '/jobs',
                   '/rvc-jobs/original', '/jobs/../audio', '/jobs/a.b/audio', '/jobs/id%2Fa/audio',
                   '/jobs/id?x=1', '/health?x=1', '/jobs/id#fragment', '/jobs/id\\audio',
                   '/jobs/id/audio/', '/jobs/id/extra/path', '/jobs/id/delete', '/jobs//audio')]
        invalid += [{'method': 'DELETE', 'path': '/jobs/original'},
                    {'method': 'POST', 'path': '/health'},
                    {'method': 'GET', 'path': '/jobs/original/cancel'},
                    {'method': 'POST', 'path': '/jobs/original/audio'},
                    {'method': 'GET', 'path': '/health', 'body_ref': BODY},
                    {'method': 'GET', 'path': '/health', 'idempotency_key': 'key'},
                    {'method': 'POST', 'path': '/jobs', 'body_ref': BODY},
                    {'method': 'POST', 'path': '/jobs/original/resume'},
                    {'method': 'POST', 'path': '/jobs', 'body_ref': BODY, 'idempotency_key': 'a\r\nb'},
                    dict(self.request('POST', '/jobs'), body={'lyrics': 'private words'}),
                    dict(self.request(), endpoint='http://localhost:8188'),
                    dict(self.request(), token='private-token')]
        for args in invalid:
            with self.subTest(args=args), self.assertRaises(ValueError):
                local.bridge_request(args, lambda _: self.fail('host called'))

    def test_request_and_reply_refs_are_exact_not_paths_or_inline_bytes(self):
        for value in ({'ref': '/private/body', 'sha256': 'a' * 64, 'bytes': 12},
                      {'ref': 'request_0', 'sha256': 'a' * 63, 'bytes': 12},
                      {'ref': 'request_0', 'sha256': 'A' * 64, 'bytes': 12},
                      dict(BODY, bytes=True), dict(BODY, bytes=-1), dict(BODY, bytes=0),
                      dict(BODY, path='/private/body'), dict(BODY, data_base64='AA=='), None):
            args = self.request('POST', '/jobs')
            args['body_ref'] = value
            with self.subTest(value=value), self.assertRaises(ValueError):
                local.bridge_request(args, lambda _: self.fail('host called'))
        for mutate in (lambda r: r['body_ref'].update(ref='request_0'),
                       lambda r: r['body_ref'].update(bytes=-1),
                       lambda r: r['body_ref'].update(sha256='no'),
                       lambda r: r.update(status=True), lambda r: r.update(status=600),
                       lambda r: r.update(mime='audio/wav\r\nsecret'),
                       lambda r: r.update(token='private'),
                       lambda r: r.update(normalized_error={'code': 'forged'})):
            out = reply()
            mutate(out)
            calls = []
            with self.subTest(out=out), self.assertRaises(ValueError):
                local.bridge_request(self.request(), lambda a: calls.append(a) or out)
            self.assertEqual(len(calls), 1)
        empty = reply()
        empty['body_ref']['bytes'] = 0
        self.assertEqual(local.bridge_request(self.request(), lambda _: empty), empty)

    def test_http_and_structured_error_classification_preserve_original_job(self):
        cases = [
            (409, 'music_account_scope_mismatch', 'service', 'yue_account_scope_mismatch'),
            (409, 'rvc_recovery_source_changed', 'input', 'rvc_recovery_failed'),
            (409, 'yue_recovery_prompt_missing_explicit_restart_required', 'input', 'yue_recovery_restart_missing_required'),
            (409, 'yue_recovery_prompt_failed_explicit_retry_required', 'input', 'yue_recovery_retry_failed_required'),
            (409, 'yue_recovery_recipe_changed', 'input', 'yue_recovery_failed'),
            (400, 'rvc_invalid_voice', 'input', 'yue_native_parameters_invalid'),
            (400, 'sampling_invalid_steps', 'input', 'yue_native_parameters_invalid'),
            (400, 'sampling_outside_native_spec:render.steps', 'input', 'yue_native_parameters_invalid'),
            (400, 'invalid_sampling_step', 'input', 'yue_native_parameters_invalid'),
            (400, 'invalid_planning_mode', 'input', 'yue_native_parameters_invalid'),
            (400, 'score_sampling_requires_active_planning', 'input', 'yue_native_parameters_invalid'),
            (401, 'unauthorized', 'service', 'yue_bridge_auth_failed'),
            (403, 'forbidden', 'service', 'yue_bridge_auth_failed'),
            (503, 'rvc_model_unavailable', 'input', 'rvc_service_not_ready'),
            (503, 'music_recovery_unavailable', 'input', 'music_recovery_unavailable'),
            (503, 'backend_stopped', 'service', 'yue_backend_unavailable'),
            (404, 'not_found', 'service', 'yue_job_not_found'),
            (409, 'not_completed', 'service', 'yue_request_rejected'),
            (500, 'unknown_error', 'service', 'yue_request_rejected'),
        ]
        for status, provider, kind, code in cases:
            observed = reply(status, {'error': provider})
            calls = []
            args = self.request('POST', '/jobs/original/resume')
            out = local.bridge_request(args, lambda a: calls.append(a) or observed)
            with self.subTest(status=status, provider=provider):
                self.assertEqual(calls, [args])
                self.assertEqual({key: out[key] for key in observed}, observed)
                self.assertEqual(out['normalized_error']['kind'], kind)
                self.assertEqual(out['normalized_error']['code'], code)
                self.assertEqual(out['normalized_error']['provider_code'], provider)
                self.assertEqual(out['normalized_error']['message'], local.ERROR_TEMPLATES[code]['message'].replace('{provider_code}', provider))
        self.assertIn('同一个job_id', local.normalize_error(reply(409, {'error': 'yue_recovery_prompt_missing_explicit_restart_required'}))['message'])

    def test_native_sampling_error_keeps_only_bounded_actual_parameter_fields(self):
        actual = 'sampling_outside_native_spec:render.steps'
        out = local.bridge_request(self.request('POST', '/jobs'), lambda _: reply(400, {'error': actual}))
        self.assertEqual(out['normalized_error']['provider_code'], actual)
        self.assertEqual(out['normalized_error']['code'], 'yue_native_parameters_invalid')
        self.assertIn(actual, out['normalized_error']['message'])
        for unsafe in ('sampling_outside_native_spec:https://example.test/?token=secret',
                       'sampling_outside_native_spec:/private/secret',
                       'sampling_outside_native_spec:C:\\private\\secret',
                       'sampling_outside_native_spec:render.steps token=secret',
                       'sampling_outside_native_spec:render.steps?token=secret',
                       'sampling_outside_native_spec:render..steps',
                       'sampling_outside_native_spec:render.steps:secret',
                       'rvc_recovery_failed:render.steps',
                       'sampling_outside_native_spec:' + 'a' * 160):
            with self.subTest(unsafe=unsafe):
                error = local.normalize_error(reply(400, {'error': unsafe}))
                self.assertEqual(error['code'], 'yue_request_rejected')
                self.assertNotIn('provider_code', error)
                self.assertNotIn(unsafe, error['message'])

    def test_arbitrary_error_text_is_not_interpolated_and_no_transport_retry(self):
        for unsafe in ('rvc_recovery_/private/path', 'rvc_recovery_secret?token=private',
                       'rvc_recovery_bad\nheaders', {'raw': 'provider details'}):
            out = local.bridge_request(self.request(), lambda _: reply(409, {'error': unsafe}))
            self.assertEqual(out['normalized_error'], {'kind': 'service', 'code': 'yue_request_rejected',
                             'message': local.ERROR_TEMPLATES['yue_request_rejected']['message']})
        calls = []
        def fail(args):
            calls.append(args)
            raise ValueError('host_service_denied_or_failed')
        with self.assertRaisesRegex(ValueError, 'host_service_denied_or_failed'):
            local.bridge_request(self.request('POST', '/jobs'), fail)
        self.assertEqual(len(calls), 1)

    def test_host_verified_json_is_classified_with_legacy_text_plain_mime(self):
        observed = reply(409, {'error': 'rvc_recovery_source_changed'}, mime='text/plain; charset=utf-8')
        out = local.bridge_request(self.request('POST', '/jobs/original/resume'), lambda _: observed)
        self.assertEqual(out['mime'], observed['mime'])
        self.assertEqual(out['json'], observed['json'])
        self.assertEqual(out['body_ref'], observed['body_ref'])
        self.assertEqual(out['normalized_error']['code'], 'rvc_recovery_failed')
        self.assertIn('rvc_recovery_source_changed', out['normalized_error']['message'])

    def test_error_templates_are_fixed_indexable_presentation_data(self):
        document = json.loads((ROOT / 'runtime/error_templates.json').read_text(encoding='utf8'))
        self.assertEqual(document['version'], 1)
        self.assertEqual(document['templates'], local.ERROR_TEMPLATES)
        self.assertEqual(len(document['templates']), 12)
        for code, template in document['templates'].items():
            with self.subTest(code=code):
                self.assertEqual(set(template), {'kind', 'message'})
                self.assertIn(template['kind'], ('service', 'input'))
                # There is one supported substitution, shared with the host;
                # templates cannot evaluate format expressions or extra input.
                text = template['message'].replace('{provider_code}', 'actual_safe_code')
                self.assertNotIn('{', text)
                self.assertNotIn('}', text)
                self.assertTrue(text)

    def test_wire_duplicate_context_never_makes_a_second_callback(self):
        context = {key: 'synthetic' for key in ('account', 'thread', 'requester', 'purpose', 'request_key')}
        args = self.request('POST', '/jobs')
        init = {'jsonrpc': '2.0', 'id': 1, 'method': 'initialize', 'params': {'_meta': {'wechat/configuration': {'values': {'local_endpoint': 'http://localhost:1'}}}}}
        call = {'jsonrpc': '2.0', 'id': 2, 'method': 'tools/call', 'params': {'name': 'music.bridge_request', 'arguments': args, '_meta': {'wechat/context': context}}}
        again = copy.deepcopy(call)
        again['id'] = 3
        for host_reply in ({'jsonrpc': '2.0', 'id': 'music-model-1', 'result': reply(200, {'id': 'original'})},
                           {'jsonrpc': '2.0', 'id': 'music-model-1', 'error': {'code': -32000, 'message': 'private error'}}):
            output = io.StringIO()
            main.Wire(io.StringIO('\n'.join(json.dumps(row) for row in (init, call, host_reply, again)) + '\n'), output).serve()
            rows = [json.loads(row) for row in output.getvalue().splitlines()]
            with self.subTest(host_reply=host_reply):
                callbacks = [row for row in rows if row.get('method') == 'wechat/music_bridge']
                self.assertEqual(len(callbacks), 1)
                self.assertEqual(callbacks[0]['params'], args)
                self.assertEqual(rows[-1]['result'], {'ok': False, 'error': 'music_bridge_request_already_used'})
                self.assertNotIn('private error', output.getvalue())
                self.assertNotIn('wechat/model_request', output.getvalue())

    def test_wire_rejects_duplicate_keys_or_missing_scope_before_callback(self):
        for raw in ('{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"arguments":{"method":"GET","method":"POST"}}}\n',
                    '{"jsonrpc":"2.0","id":1,"method":"initialize"}\n{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"music.bridge_request","arguments":{"method":"GET","path":"/health"}}}\n'):
            output = io.StringIO()
            main.Wire(io.StringIO(raw), output).serve()
            self.assertNotIn('wechat/music_bridge', output.getvalue())

    def test_package_local_capability_is_internal_and_config_independent(self):
        config = json.loads((ROOT / 'config.schema.json').read_text(encoding='utf8'))
        package = json.loads((ROOT / 'plugin.json').read_text(encoding='utf8'))
        caps = json.loads((ROOT / 'capabilities.json').read_text(encoding='utf8'))['capabilities']
        self.assertFalse(config.get('required'))
        self.assertTrue(config['properties']['local_token']['x-host-only'])
        self.assertTrue(config['properties']['local_token']['x-secret'])
        self.assertEqual(package['permissions'], ['model.invoke', 'network'])
        self.assertTrue(package['http_routes'][0]['optional'])
        local_cap = next(cap for cap in caps if cap['id'] == 'music.bridge_request')
        self.assertEqual(local_cap['permissions'], ['network'])
        self.assertFalse(local_cap.get('async', False))
        with self.assertRaisesRegex(ValueError, 'music_model_not_configured'):
            main.cloud_music.generate({'prompt': 'synthetic'}, {'local_endpoint': 'http://localhost:1'}, lambda _: self.fail('cloud called'))
        out = local.bridge_request(self.request(), lambda _: reply())
        self.assertEqual(out['status'], 200)


if __name__ == '__main__':
    unittest.main()
