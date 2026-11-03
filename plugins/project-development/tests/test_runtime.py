import importlib.util
import json
from pathlib import Path
import sys
import unittest

# Package directories are integrity-sealed. Test discovery may import runtime
# source directly, so forbid a stray __pycache__ member from invalidating the
# package manifest on Windows or in an IDE test runner.
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location('project_runtime', Path(__file__).parents[1] / 'runtime' / 'main.py')
runtime = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runtime)


class ProjectRuntimeTests(unittest.TestCase):
    def test_every_tool_is_mapped_and_request_parameters_are_preserved(self):
        for name, tool in runtime.TOOLS.items():
            args = {'path': '/synthetic/project', 'length': 524288, 'limit': 12345, 'max_files': 0, 'timeout_seconds': 0, 'cursor': 'original-cursor', 'command': 'echo synthetic', 'request_key': 'durable-key'}
            if name == 'project_create':
                args = {'name': 'new-project', 'shared': True}
            elif name == 'project_read':
                args = {'id': 'known-project'}
            elif name == 'project_list':
                args = {}
            elif name == 'server_tools':
                args = {'classes': ['media'], 'id': 'media.ffmpeg'}
            calls = []

            def host(request):
                calls.append(request)
                if request['operation'] == 'snapshot':
                    return {'records': [{'name': 'known-project.json', 'content': '{"id":"known-project"}'}], 'execution': {'account': 'test'}}
                return {'value': {'state': 'synthetic-observation'}}

            result = runtime.execute({'tool': name, 'arguments': args}, host)
            self.assertEqual(calls, [{'tool': name, 'operation': tool['operation'], 'arguments': args}])
            self.assertEqual(result['error'], '')

    def test_manifest_parsing_reports_invalid_records_without_losing_valid_ones(self):
        observation = {'execution': {'account': 'test'}, 'records': [
            {'name': 'valid.json', 'content': '{"id":"valid","usage":"synthetic"}'},
            {'name': 'broken.json', 'content': '{'},
            {'name': 'array.json', 'content': '[]'},
            {'name': 'large.json', 'too_large': True},
            {'name': 'link.json', 'error': 'project_manifest_unavailable'},
        ]}
        result = runtime.execute({'tool': 'project_list', 'arguments': {}}, lambda _: observation)
        self.assertEqual(result['value']['projects'], [{'id': 'valid', 'usage': 'synthetic'}])
        self.assertEqual(len(result['value']['unavailable_manifests']), 4)
        self.assertEqual(result['observation'], observation)

    def test_empty_catalog_and_missing_project(self):
        host = lambda _: {'records': [], 'execution': {'account': 'test'}}
        self.assertEqual(runtime.execute({'tool': 'project_list', 'arguments': {}}, host)['value']['projects'], [])
        self.assertEqual(runtime.execute({'tool': 'project_read', 'arguments': {'id': 'missing'}}, host)['error'], 'project_not_registered')

    def test_invalid_names_fail_before_host_io(self):
        def unexpected(_):
            self.fail('invalid name reached host')
        for name in ('', '..', '../escape', 'a/b', 'con', 'COM1', 'nul'):
            with self.assertRaises(ValueError):
                runtime.execute({'tool': 'project_create', 'arguments': {'name': name}}, unexpected)
        for name in ('', '..', '../escape', 'a\\b', 'valid:stream'):
            with self.assertRaises(ValueError):
                runtime.execute({'tool': 'project_read', 'arguments': {'id': name}}, unexpected)

    def test_host_errors_never_become_successful_writes(self):
        observed = {'error': 'original host permission failure'}
        result = runtime.execute({'tool': 'server_write', 'arguments': {'path': '/synthetic/a', 'content': 'synthetic'}}, lambda _: observed)
        self.assertEqual(result['error'], observed['error'])
        self.assertIsNone(result['value'])

    def test_packaged_tool_index_keeps_exact_selection(self):
        args = {'classes': ['python'], 'id': 'python.cli'}
        calls = []
        result = runtime.execute({'tool': 'server_tools', 'arguments': args}, lambda request: calls.append(request) or {'value': {'tools': []}})
        self.assertEqual(calls, [{'tool': 'server_tools', 'operation': 'tool_index', 'arguments': args}])
        self.assertEqual(result['value'], {'tools': []})


if __name__ == '__main__':
    unittest.main()
