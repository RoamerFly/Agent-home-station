"""Credential-free, single-phase external HTTP tool runtime."""
import base64
import json
import re
import sys

FRAME_LIMIT = 8 * 1024 * 1024
# Legacy Runtime tool names are <plugin-id>__<tool-id>: both components permit
# 64 bytes, so the credential-free projected name can be 130 bytes.
NAME = re.compile(r"^[A-Za-z][A-Za-z0-9_-]{0,129}$")


def object_of(value, code):
    if not isinstance(value, dict):
        raise ValueError(code)
    return value


def at(value, dotted):
    for key in dotted.split('.'):
        if key:
            if not isinstance(value, dict) or key not in value:
                return ''
            value = value[key]
    return value


def text(value, nested=False):
    # Match Go's stringAt semantics: null means absent; booleans are lowercase;
    # parse_int/parse_float below retain the provider's numeric lexeme.
    if value is None: return '<nil>' if nested else ''
    if value == '': return ''
    if value is True: return 'true'
    if value is False: return 'false'
    if isinstance(value, dict):
        return 'map[' + ' '.join(str(key) + ':' + text(value[key], True) for key in sorted(value)) + ']'
    if isinstance(value, list):
        return '[' + ' '.join(text(item, True) for item in value) + ']'
    return value if isinstance(value, str) else str(value)


def protocol(raw):
    """Validate the host's credential-free legacy-tool projection.

    The projection is supplied per invocation, not as mutable process metadata:
    the host policy must bind it to the exact installed configuration revision.
    """
    doc = object_of(raw, 'external_protocol_invalid')
    if doc.get('version') != 1 or set(doc) != {'version', 'tools'} or not isinstance(doc['tools'], list) or len(doc['tools']) > 80:
        raise ValueError('external_protocol_invalid')
    out = {}
    for item in doc['tools']:
        item = object_of(item, 'external_protocol_invalid')
        if set(item) - {'name', 'description', 'input_schema', 'text_path', 'media_url_path', 'media_kind', 'fingerprint'}:
            raise ValueError('external_protocol_invalid')
        name, schema = item.get('name'), item.get('input_schema')
        if not isinstance(name, str) or not NAME.fullmatch(name) or name in out or not isinstance(item.get('description'), str) or not isinstance(schema, dict) or schema.get('type') != 'object' or not isinstance(item.get('fingerprint'), str) or not re.fullmatch(r'[a-f0-9]{64}', item['fingerprint']):
            raise ValueError('external_protocol_invalid')
        if not isinstance(item.get('text_path', ''), str) or not isinstance(item.get('media_url_path', ''), str) or item.get('media_kind', '') not in ('', 'image', 'voice', 'video', 'file'):
            raise ValueError('external_protocol_invalid')
        if item.get('media_url_path') and not item.get('media_kind'):
            raise ValueError('external_protocol_invalid')
        out[name] = {'name': name, 'description': item['description'], 'input_schema': schema, 'text_path': item.get('text_path', ''), 'media_url_path': item.get('media_url_path', ''), 'media_kind': item.get('media_kind', ''), 'fingerprint': item['fingerprint']}
    return out


def parse_reply(reply, tool):
    reply = object_of(reply, 'external_host_response_invalid')
    if not isinstance(reply.get('content_type'), str) or set(reply) not in ({'content_type', 'body_base64'}, {'content_type', 'binary_ref'}):
        raise ValueError('external_host_response_invalid')
    if 'binary_ref' in reply:
        ref = object_of(reply['binary_ref'], 'external_host_response_invalid')
        if set(ref) != {'sha256', 'size'} or not isinstance(ref['sha256'], str) or not re.fullmatch(r'[a-f0-9]{64}', ref['sha256']) or not isinstance(ref['size'], int) or ref['size'] < 1:
            raise ValueError('external_host_response_invalid')
        if not tool['media_kind']:
            raise ValueError('external_binary_without_media_kind')
        return {'observation': reply, 'output': {'text':'', 'media':[{'kind':tool['media_kind'], 'host_binary_ref':ref['sha256']}]}}
    if not isinstance(reply['body_base64'], str):
        raise ValueError('external_host_response_invalid')
    try:
        body = base64.b64decode(reply['body_base64'], validate=True)
    except ValueError as error:
        raise ValueError('external_host_response_invalid') from error
    output = {'text': '', 'media': []}
    declared_json = 'json' in reply['content_type'].lower()
    try:
        # Match the legacy isJSON contract: JSON scalars and null are JSON even
        # with text/plain, while a declared JSON content type never falls back
        # to binary after a parse failure.
        decoded = json.loads(body, parse_int=str, parse_float=str)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        if declared_json:
            raise ValueError('external_response_invalid') from error
        decoded = None
    if decoded is None and not declared_json and body.strip() != b'null':
        if not tool['media_kind']:
            raise ValueError('external_binary_without_media_kind')
        output['media'] = [{'kind': tool['media_kind'], 'data_base64': reply['body_base64'], 'mime_type': reply['content_type']}]
        return {'observation': reply, 'output': output}
    if tool['text_path']:
        value = at(decoded, tool['text_path'])
        output['text'] = text(value)
    if tool['media_url_path']:
        url = at(decoded, tool['media_url_path'])
        if not isinstance(url, str) or not url:
            raise ValueError('external_response_has_no_safe_output')
        output['media'] = [{'kind':tool['media_kind'], 'url':url}]
    if not output['text'] and not output['media']:
        raise ValueError('external_response_has_no_safe_output')
    return {'observation': reply, 'output': output}


def execute(arguments, host):
    request = object_of(arguments, 'external_arguments_invalid')
    if set(request) != {'phase', 'tool', 'arguments', 'arguments_sha256', 'protocol'} or request.get('phase') != 'call' or not isinstance(request.get('tool'), str) or not isinstance(request.get('arguments'), dict) or not isinstance(request.get('arguments_sha256'), str) or not re.fullmatch(r'[a-f0-9]{64}', request['arguments_sha256']):
        raise ValueError('external_operation_invalid')
    tools = protocol(request['protocol'])
    tool = tools.get(request['tool'])
    if not tool:
        raise ValueError('external_tool_unavailable')
    # The plugin sends no URL, header, endpoint, credential or protocol back.
    # The host holds the immutable canonical raw arguments. Forward only its
    # opaque digest so provider numbers can never be changed by a Python JSON
    # parse/dump round trip.
    reply = host({'phase': 'call', 'tool': tool['name'], 'arguments_sha256': request['arguments_sha256']})
    return parse_reply(reply, tool)


class Wire:
    def __init__(self, incoming, outgoing):
        self.incoming, self.outgoing, self.sequence = incoming, outgoing, 0
    def read(self):
        line = self.incoming.readline(FRAME_LIMIT + 1)
        if not line: raise EOFError()
        if not line.endswith('\n') or len(line.encode('utf8')) > FRAME_LIMIT: raise ValueError('frame_limit')
        return object_of(json.loads(line), 'invalid_protocol')
    def write(self, value):
        raw = json.dumps(value, ensure_ascii=False, separators=(',', ':'))
        if len(raw.encode('utf8')) + 1 > FRAME_LIMIT: raise ValueError('frame_limit')
        self.outgoing.write(raw + '\n'); self.outgoing.flush()
    def host(self, params):
        self.sequence += 1; identity = 'external-request-' + str(self.sequence)
        self.write({'jsonrpc':'2.0', 'id':identity, 'method':'wechat/external_request', 'params':params})
        reply = self.read()
        if reply.get('jsonrpc') != '2.0' or reply.get('id') != identity or 'error' in reply or not isinstance(reply.get('result'), dict): raise ValueError('external_host_request_failed')
        return reply['result']
    def serve(self):
        initialized = False
        schema = {'type':'object','properties':{'phase':{'type':'string','enum':['call']},'tool':{'type':'string'},'arguments':{'type':'object'},'arguments_sha256':{'type':'string','pattern':'^[a-f0-9]{64}$'},'protocol':{'type':'object'}},'required':['phase','tool','arguments','arguments_sha256','protocol'],'additionalProperties':False}
        while True:
            try: request = self.read()
            except EOFError: return
            except (ValueError, json.JSONDecodeError): continue
            if 'id' not in request: continue
            try:
                if request.get('jsonrpc') != '2.0': raise ValueError('invalid_protocol')
                method = request.get('method')
                if method == 'initialize':
                    initialized = True; result = {'protocolVersion':'2025-06-18','capabilities':{'tools':{}},'serverInfo':{'name':'external-tools','version':'0.3.0'}}
                elif initialized and method == 'ping': result = {}
                elif initialized and method == 'tools/list': result = {'tools':[{'name':'external.http_request','description':'Run one exact host-bound synchronous HTTP tool phase.', 'inputSchema':schema}]}
                elif initialized and method == 'tools/call':
                    params = object_of(request.get('params'), 'external_arguments_invalid')
                    context = object_of(params.get('_meta', {}).get('wechat/context'), 'host_context_required')
                    if params.get('name') != 'external.http_request' or not all(isinstance(context.get(k), str) and context[k] for k in ('account','thread','requester','purpose','request_key')): raise ValueError('host_context_required')
                    result = execute(params.get('arguments'), self.host)
                else: raise ValueError('method_unavailable')
            except (ValueError, TypeError, KeyError) as error:
                code = str(error); result = {'ok':False,'error':code if re.fullmatch(r'[a-z_]{1,96}', code) else 'external_request_invalid'}
            self.write({'jsonrpc':'2.0','id':request['id'],'result':result})

if __name__ == '__main__':
    sys.stdin.reconfigure(encoding='utf8'); sys.stdout.reconfigure(encoding='utf8'); Wire(sys.stdin, sys.stdout).serve()
