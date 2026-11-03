"""Project catalog, registration parsing and operation mapping. No OS grants."""
import json
from pathlib import Path
import re
import sys

CATALOG = json.loads(Path(__file__).with_name('catalog.json').read_text('utf8'))['tools']
TOOLS = {row['name']: row for row in CATALOG}
FRAME_LIMIT = 8 * 1024 * 1024


def execute(arguments, host):
    if not isinstance(arguments, dict) or set(arguments) != {'tool', 'arguments'}:
        raise ValueError('invalid_request')
    tool, args = arguments['tool'], arguments['arguments']
    if tool not in TOOLS or not isinstance(args, dict):
        raise ValueError('tool_unavailable')
    if tool == 'project_create':
        name = args.get('name', '')
        if not re.fullmatch(r'[a-z][a-z0-9_-]{0,47}', name) or re.fullmatch(r'(con|prn|aux|nul|com[0-9]|lpt[0-9])', name):
            raise ValueError('invalid_project_name')
    if tool == 'project_read':
        name = args.get('id', '')
        if not isinstance(name, str) or not name or name in ('.', '..') or any(c in name for c in '/\\:\0'):
            raise ValueError('invalid_project_id')
    observation = host({'tool': tool, 'operation': TOOLS[tool]['operation'], 'arguments': args})
    value = observation.get('value')
    failure = observation.get('error')
    if tool in ('project_list', 'project_read') and not failure:
        projects, issues = [], []
        for record in observation['records']:
            reason = record.get('error')
            if not reason:
                try:
                    if record.get('too_large'):
                        raise ValueError('project_manifest_metadata_too_large')
                    obj = json.loads(record['content'])
                    if not isinstance(obj, dict):
                        raise ValueError('project_manifest_invalid')
                    projects.append(obj)
                except (ValueError, TypeError, KeyError) as e:
                    reason = str(e) if str(e) == 'project_manifest_metadata_too_large' else 'project_manifest_invalid'
            if reason:
                issues.append({'name': record['name'], 'reason': reason})
        if tool == 'project_read':
            if len(projects) != 1:
                failure = issues[0]['reason'] if issues else 'project_not_registered'
            else:
                value = projects[0]
        else:
            value = {'projects': projects, 'unavailable_manifests': issues, 'execution': observation['execution']}
    return {'observation': observation, 'value': value, 'error': failure or ''}


class Wire:
    def __init__(self, incoming, outgoing):
        self.incoming, self.outgoing, self.sequence = incoming, outgoing, 0

    def read(self):
        line = self.incoming.readline(FRAME_LIMIT + 1)
        if not line:
            raise EOFError()
        if not line.endswith('\n') or len(line.encode('utf8')) > FRAME_LIMIT:
            raise ValueError('frame_limit')
        value = json.loads(line)
        if not isinstance(value, dict):
            raise ValueError('invalid_frame')
        return value

    def write(self, value):
        line = json.dumps(value, ensure_ascii=False, separators=(',', ':'))
        if len(line.encode('utf8')) + 1 > FRAME_LIMIT:
            raise ValueError('frame_limit')
        self.outgoing.write(line + '\n')
        self.outgoing.flush()

    def host(self, args):
        self.sequence += 1
        identity = 'project-workspace-' + str(self.sequence)
        self.write({'jsonrpc': '2.0', 'id': identity, 'method': 'wechat/workspace_request', 'params': args})
        response = self.read()
        if response.get('id') != identity or response.get('jsonrpc') != '2.0' or 'error' in response or not isinstance(response.get('result'), dict):
            raise ValueError('host_request_failed')
        return response['result']

    def serve(self):
        initialized = False
        while True:
            try:
                request = self.read()
            except EOFError:
                return
            if 'id' not in request:
                continue
            try:
                method = request.get('method')
                if method == 'initialize':
                    initialized = True
                    result = {'protocolVersion': '2025-06-18', 'capabilities': {'tools': {}}, 'serverInfo': {'name': 'project-development', 'version': '0.2.1'}}
                elif initialized and method == 'ping':
                    result = {}
                elif initialized and method == 'tools/list':
                    result = {'tools': [{'name': name, 'description': 'Perform a host-bound project operation.', 'inputSchema': {'type': 'object'}} for name in ('project.workspace_read', 'project.workspace_write', 'project.workspace_execute')]}
                elif initialized and method == 'tools/call':
                    params = request.get('params', {})
                    context = params.get('_meta', {}).get('wechat/context', {})
                    if params.get('name') != {'workspace.read':'project.workspace_read','workspace.write':'project.workspace_write','process.execute':'project.workspace_execute'}.get(TOOLS.get(params.get('arguments', {}).get('tool'), {}).get('permission')) or not all(context.get(key) for key in ('account', 'thread', 'requester', 'purpose', 'request_key')):
                        raise ValueError('host_context_required')
                    result = execute(params.get('arguments'), self.host)
                else:
                    raise ValueError('method_unavailable')
            except (ValueError, KeyError, TypeError) as e:
                result = {'error': str(e)}
            self.write({'jsonrpc': '2.0', 'id': request['id'], 'result': result})


if __name__ == '__main__':
    sys.stdin.reconfigure(encoding='utf8')
    sys.stdout.reconfigure(encoding='utf8')
    Wire(sys.stdin, sys.stdout).serve()
