"""Credential-free, single-phase external HTTP tool runtime."""
import base64
import json
import re
import sys

FRAME_LIMIT = 8 * 1024 * 1024
# Legacy Runtime tool names are <plugin-id>__<tool-id>: both components permit
# 64 bytes, so the credential-free projected name can be 130 bytes.
NAME = re.compile(r"^[A-Za-z][A-Za-z0-9_-]{0,129}$")
RAW_TOOL = re.compile(r"^[A-Za-z][A-Za-z0-9_-]{0,63}$")


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


JOB_ID = re.compile(r'^[A-Za-z0-9][A-Za-z0-9_.:-]{0,255}$')

def job_protocol(raw):
    doc = object_of(raw, 'external_job_protocol_invalid')
    if doc.get('version') != 1 or set(doc) != {'version','tool'}:
        raise ValueError('external_job_protocol_invalid')
    item = dict(object_of(doc['tool'], 'external_job_protocol_invalid'))
    job, poll = item.pop('job', None), item.pop('poll', None)
    tools = protocol({'version':1,'tools':[item]})
    tool = next(iter(tools.values()))
    job, poll = object_of(job, 'external_job_protocol_invalid'), object_of(poll, 'external_job_protocol_invalid')
    if set(job) != {'id_path'} or not isinstance(job.get('id_path'), str) or not job['id_path'] or set(poll) - {'status_path','done_values','failed_values','text_path','media_url_path','media_kind'} or not isinstance(poll.get('status_path'), str) or not poll['status_path'] or not isinstance(poll.get('done_values'), list) or not poll['done_values'] or not all(isinstance(x,str) for x in poll['done_values']) or not all(isinstance(x,str) for x in poll.get('failed_values',[])):
        raise ValueError('external_job_protocol_invalid')
    tool['job'], tool['poll'] = job, {'status_path':poll['status_path'],'done_values':poll['done_values'],'failed_values':poll.get('failed_values',[]),'text_path':poll.get('text_path',tool['text_path']),'media_url_path':poll.get('media_url_path',tool['media_url_path']),'media_kind':poll.get('media_kind',tool['media_kind'])}
    return tool

def job_ticket(raw, tool):
    item = object_of(raw, 'external_ticket_invalid')
    if set(item) - {'tool','fingerprint','remote_id','state','output'} or item.get('tool') != tool['name'] or item.get('fingerprint') != tool['fingerprint'] or item.get('state') not in ('submitting','waiting','completed','failed') or not isinstance(item.get('remote_id',''),str):
        raise ValueError('external_ticket_invalid')
    return dict(item)

def job_json(reply):
    reply = object_of(reply, 'external_host_response_invalid')
    if set(reply) != {'content_type','body_base64'} or not isinstance(reply['body_base64'],str): raise ValueError('external_host_response_invalid')
    try: return reply, json.loads(base64.b64decode(reply['body_base64'],validate=True),parse_int=str,parse_float=str)
    except (ValueError,UnicodeDecodeError,json.JSONDecodeError) as error: raise ValueError('external_response_invalid') from error

def job_result(ticket, tool, observation, value):
    state = text(at(value,tool['poll']['status_path']))
    if not state:
        raise ValueError('external_status_missing')
    # Legacy jobResult checks failed states before done states.
    if state in tool['poll']['failed_values']:
        ticket['state'], ticket['output'] = 'failed', {'text':'','media':[]}
        return {'observation':observation,'ticket':ticket}
    if state not in tool['poll']['done_values']:
        ticket['state'], ticket['output'] = 'waiting', {'text':'','media':[]}
        if not JOB_ID.fullmatch(ticket.get('remote_id','')):
            raise ValueError('external_remote_id_invalid')
        return {'observation':observation,'ticket':ticket}
    extract = dict(tool); extract.update({'text_path':tool['poll']['text_path'],'media_url_path':tool['poll']['media_url_path'],'media_kind':tool['poll']['media_kind']})
    synthetic = {'content_type':'application/json','body_base64':observation['body_base64']}
    ticket['state'], ticket['output'] = 'completed', parse_reply(synthetic,extract)['output']
    return {'observation':observation,'ticket':ticket}

def job_execute(arguments, host):
    request = object_of(arguments, 'external_arguments_invalid')
    if set(request) != {'phase','tool','arguments','arguments_sha256','ticket','ticket_sha256','protocol'} or request.get('phase') not in ('start','poll','cancel') or not isinstance(request.get('tool'),str) or not isinstance(request.get('arguments'),dict) or not isinstance(request.get('arguments_sha256'),str) or not re.fullmatch(r'[a-f0-9]{64}',request['arguments_sha256']) or not isinstance(request.get('ticket_sha256'),str) or not re.fullmatch(r'[a-f0-9]{64}',request['ticket_sha256']): raise ValueError('external_operation_invalid')
    tool = job_protocol(request['protocol'])
    if request['tool'] != tool['name']: raise ValueError('external_tool_unavailable')
    ticket = job_ticket(request['ticket'],tool)
    phase = request['phase']
    if phase == 'start' and (ticket['state'] != 'submitting' or ticket.get('remote_id')): raise ValueError('external_ticket_invalid')
    if phase == 'poll' and (ticket['state'] != 'waiting' or not JOB_ID.fullmatch(ticket.get('remote_id',''))): raise ValueError('external_ticket_invalid')
    if phase == 'cancel' and not JOB_ID.fullmatch(ticket.get('remote_id','')): raise ValueError('external_ticket_invalid')
    reply = host({'phase':phase,'tool':tool['name'],'arguments_sha256':request['arguments_sha256'],'ticket_sha256':request['ticket_sha256']})
    if phase == 'cancel': return {'observation':reply,'cancel_ack':True,'ticket':ticket}
    observation, value = job_json(reply)
    if phase == 'start':
        remote = at(value,tool['job']['id_path'])
        if isinstance(remote,(str,int)) and JOB_ID.fullmatch(str(remote)): ticket['remote_id'] = str(remote)
        try:
            return job_result(ticket,tool,observation,value)
        except ValueError:
            status = text(at(value,tool['poll']['status_path']))
            # Match legacy StartJob: a valid acknowledged remote identity is
            # retained when an immediate completed response cannot be safely
            # extracted.  Do not hide provider failure or invent an ID.
            if ticket.get('remote_id') and status not in tool['poll']['failed_values']:
                ticket['state'], ticket['output'] = 'waiting', {'text':'','media':[]}
                return {'observation':observation,'ticket':ticket,'known_remote_only':True}
            raise
    return job_result(ticket,tool,observation,value)


def poll_protocol(raw):
    """Validate the durable projection of a legacy PollSpec without Job."""
    doc = object_of(raw, 'external_poll_protocol_invalid')
    if doc.get('version') != 1 or set(doc) != {'version','tool'}:
        raise ValueError('external_poll_protocol_invalid')
    item = dict(object_of(doc['tool'], 'external_poll_protocol_invalid'))
    job, poll = item.pop('job', None), item.pop('poll', None)
    if job is not None:
        raise ValueError('external_poll_protocol_invalid')
    tools = protocol({'version':1,'tools':[item]})
    tool = next(iter(tools.values()))
    poll = object_of(poll, 'external_poll_protocol_invalid')
    allowed = {'status_path','done_values','failed_values','text_path','media_url_path','media_kind','interval_seconds','max_attempts','timeout_seconds'}
    if set(poll) - allowed or not isinstance(poll.get('status_path'),str) or not poll['status_path'] or not isinstance(poll.get('done_values'),list) or not poll['done_values'] or not all(isinstance(x,str) for x in poll['done_values']) or not all(isinstance(x,str) for x in poll.get('failed_values',[])):
        raise ValueError('external_poll_protocol_invalid')
    for key in ('interval_seconds','max_attempts','timeout_seconds'):
        if key in poll and (not isinstance(poll[key],int) or isinstance(poll[key],bool) or poll[key] < 0):
            raise ValueError('external_poll_protocol_invalid')
    if not isinstance(poll.get('text_path',tool['text_path']),str) or not isinstance(poll.get('media_url_path',tool['media_url_path']),str) or poll.get('media_kind',tool['media_kind']) not in ('', 'image', 'voice', 'video', 'file'):
        raise ValueError('external_poll_protocol_invalid')
    tool['poll'] = {'status_path':poll['status_path'],'done_values':poll['done_values'],'failed_values':poll.get('failed_values',[]),'text_path':poll.get('text_path',tool['text_path']),'media_url_path':poll.get('media_url_path',tool['media_url_path']),'media_kind':poll.get('media_kind',tool['media_kind'])}
    return tool


def poll_result(ticket, tool, observation, value):
    # The pre-Job Runtime.Call treated every state outside done/failed as a
    # pending poll; preserve that behavior without an in-process sleep/loop.
    state = text(at(value, tool['poll']['status_path']))
    ticket['remote_id'], ticket['output'] = '', {'text':'','media':[]}
    if state in tool['poll']['failed_values']:
        ticket['state'] = 'failed'
        return {'observation':observation,'ticket':ticket}
    if state not in tool['poll']['done_values']:
        ticket['state'] = 'waiting'
        return {'observation':observation,'ticket':ticket}
    extract = dict(tool)
    extract.update({'text_path':tool['poll']['text_path'],'media_url_path':tool['poll']['media_url_path'],'media_kind':tool['poll']['media_kind']})
    synthetic = {'content_type':'application/json','body_base64':observation['body_base64']}
    ticket['state'] = 'completed'
    return {'observation':observation,'ticket':ticket,'output':parse_reply(synthetic,extract)['output']}


def poll_extract_tool(tool):
    extract = dict(tool)
    extract.update({'text_path':tool['poll']['text_path'],'media_url_path':tool['poll']['media_url_path'],'media_kind':tool['poll']['media_kind']})
    return extract


def poll_execute(arguments, host):
    request = object_of(arguments, 'external_arguments_invalid')
    required = {'phase','tool','arguments','arguments_sha256','ticket','ticket_sha256','protocol'}
    if set(request) != required or request.get('phase') not in ('start','query') or not isinstance(request.get('tool'),str) or not isinstance(request.get('arguments'),dict) or not all(isinstance(request.get(x),str) and re.fullmatch(r'[a-f0-9]{64}',request[x]) for x in ('arguments_sha256','ticket_sha256')):
        raise ValueError('external_poll_operation_invalid')
    tool = poll_protocol(request['protocol'])
    if request['tool'] != tool['name']:
        raise ValueError('external_tool_unavailable')
    ticket = job_ticket(request['ticket'],tool)
    if ticket.get('remote_id'):
        raise ValueError('external_ticket_invalid')
    phase = request['phase']
    if phase == 'start' and ticket['state'] != 'submitting':
        raise ValueError('external_ticket_invalid')
    if phase == 'query' and ticket['state'] != 'waiting':
        raise ValueError('external_ticket_invalid')
    reply = host({'phase':phase,'tool':tool['name'],'arguments_sha256':request['arguments_sha256'],'ticket_sha256':request['ticket_sha256']})
    # Legacy Runtime.Call returned an initial non-JSON media response directly
    # before entering its Poll loop. Preserve that immediate completion without
    # reading large binary payloads through stdio; query responses remain JSON.
    if phase == 'start' and isinstance(reply,dict) and 'binary_ref' in reply:
        parsed = parse_reply(reply, poll_extract_tool(tool))
        ticket['state'], ticket['remote_id'], ticket['output'] = 'completed', '', {'text':'','media':[]}
        return {'observation':parsed['observation'],'ticket':ticket,'output':parsed['output']}
    observation, value = job_json(reply)
    return poll_result(ticket, tool, observation, value)

def mcp_execute(arguments, host):
    request = object_of(arguments,'external_mcp_invalid')
    required = {'operation','server_id','arguments','arguments_sha256','request_sha256','protocol'}
    if set(request) - (required | {'tool'}) or not required.issubset(request) or request.get('operation') not in ('list','call') or not isinstance(request.get('server_id'),str) or not NAME.fullmatch(request['server_id']) or not isinstance(request['arguments'],dict) or not all(isinstance(request.get(x),str) and re.fullmatch(r'[a-f0-9]{64}',request[x]) for x in ('arguments_sha256','request_sha256')):
        raise ValueError('external_mcp_invalid')
    protocol = object_of(request['protocol'],'external_mcp_invalid')
    if set(protocol) != {'version','server_id','allowlist'} or protocol.get('version') != 1 or protocol.get('server_id') != request['server_id'] or not isinstance(protocol.get('allowlist'),list) or not all(isinstance(x,str) and RAW_TOOL.fullmatch(x) for x in protocol['allowlist']): raise ValueError('external_mcp_invalid')
    allowed = lambda name: not protocol['allowlist'] or name in protocol['allowlist']
    if request['operation'] == 'call' and (not isinstance(request.get('tool'),str) or not RAW_TOOL.fullmatch(request['tool']) or not allowed(request['tool'])): raise ValueError('external_mcp_tool_unavailable')
    if request['operation'] == 'list' and 'tool' in request: raise ValueError('external_mcp_invalid')
    envelope = object_of(host({'operation':request['operation'],'request_sha256':request['request_sha256']}),'external_mcp_response_invalid')
    if set(envelope) != {'raw_result','body_ref'}: raise ValueError('external_mcp_response_invalid')
    observation = object_of(envelope['body_ref'],'external_mcp_response_invalid')
    raw = object_of(envelope['raw_result'],'external_mcp_response_invalid')
    if request['operation'] == 'list':
        tools=[]
        for item in raw.get('tools',[]):
            if not isinstance(item,dict) or not isinstance(item.get('name'),str) or not RAW_TOOL.fullmatch(item['name']) or not allowed(item['name']): continue
            description=item.get('description',''); schema=item.get('inputSchema',{'type':'object'})
            if not isinstance(description,str) or not isinstance(schema,dict): raise ValueError('external_mcp_schema_invalid')
            tools.append({'name':request['server_id']+'__'+item['name'],'description':description,'input_schema':schema})
        out={'observation':observation,'tools':tools}
        if isinstance(raw.get('nextCursor'),str): out['next_cursor']=raw['nextCursor']
        return out
    content=raw.get('content')
    if not isinstance(content,list): raise ValueError('external_mcp_response_invalid')
    text_parts=[]; media=[]
    for index,item in enumerate(content):
        if not isinstance(item,dict): raise ValueError('external_mcp_content_type_requires_resource_reference')
        if item.get('type') == 'text' and isinstance(item.get('text'),str): text_parts.append(item['text'])
        elif item.get('type') in ('image','audio') and isinstance(item.get('host_media_ref'),str) and isinstance(item.get('mimeType'),str): media.append({'kind':'voice' if item['type']=='audio' else 'image','content_index':index,'host_media_ref':item['host_media_ref'],'mime_type':item['mimeType']})
        elif item.get('type') != 'text': raise ValueError('external_mcp_content_type_requires_resource_reference')
    return {'observation':observation,'output':{'text':'\n'.join(text_parts),'media':media},'is_error':bool(raw.get('isError'))}


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
    def mcp_host(self, params):
        self.sequence += 1; identity = 'external-mcp-' + str(self.sequence)
        self.write({'jsonrpc':'2.0','id':identity,'method':'wechat/external_mcp','params':params})
        reply = self.read()
        if reply.get('jsonrpc') != '2.0' or reply.get('id') != identity or 'error' in reply or not isinstance(reply.get('result'),dict): raise ValueError('external_mcp_request_failed')
        return reply['result']
    def serve(self):
        initialized = False
        schema = {'type':'object','properties':{'phase':{'type':'string','enum':['call']},'tool':{'type':'string'},'arguments':{'type':'object'},'arguments_sha256':{'type':'string','pattern':'^[a-f0-9]{64}$'},'protocol':{'type':'object'}},'required':['phase','tool','arguments','arguments_sha256','protocol'],'additionalProperties':False}
        job_schema = {'type':'object','properties':{'phase':{'type':'string','enum':['start','poll','cancel']},'tool':{'type':'string'},'arguments':{'type':'object'},'arguments_sha256':{'type':'string','pattern':'^[a-f0-9]{64}$'},'ticket':{'type':'object'},'ticket_sha256':{'type':'string','pattern':'^[a-f0-9]{64}$'},'protocol':{'type':'object'}},'required':['phase','tool','arguments','arguments_sha256','ticket','ticket_sha256','protocol'],'additionalProperties':False}
        poll_schema = {'type':'object','properties':{'phase':{'type':'string','enum':['start','query']},'tool':{'type':'string'},'arguments':{'type':'object'},'arguments_sha256':{'type':'string','pattern':'^[a-f0-9]{64}$'},'ticket':{'type':'object'},'ticket_sha256':{'type':'string','pattern':'^[a-f0-9]{64}$'},'protocol':{'type':'object'}},'required':['phase','tool','arguments','arguments_sha256','ticket','ticket_sha256','protocol'],'additionalProperties':False}
        mcp_schema = {'type':'object','properties':{'operation':{'type':'string','enum':['list','call']},'server_id':{'type':'string'},'tool':{'type':'string'},'arguments':{'type':'object'},'arguments_sha256':{'type':'string','pattern':'^[a-f0-9]{64}$'},'request_sha256':{'type':'string','pattern':'^[a-f0-9]{64}$'},'protocol':{'type':'object'}},'required':['operation','server_id','arguments','arguments_sha256','request_sha256','protocol'],'additionalProperties':False}
        while True:
            try: request = self.read()
            except EOFError: return
            except (ValueError, json.JSONDecodeError): continue
            if 'id' not in request: continue
            try:
                if request.get('jsonrpc') != '2.0': raise ValueError('invalid_protocol')
                method = request.get('method')
                if method == 'initialize':
                    initialized = True; result = {'protocolVersion':'2025-06-18','capabilities':{'tools':{}},'serverInfo':{'name':'external-tools','version':'0.6.0'}}
                elif initialized and method == 'ping': result = {}
                elif initialized and method == 'tools/list': result = {'tools':[{'name':'external.http_request','description':'Run one exact host-bound synchronous HTTP tool phase.', 'inputSchema':schema},{'name':'external.job_request','description':'Run exactly one host-bound legacy job start, poll, or cancel phase.', 'inputSchema':job_schema},{'name':'external.poll_request','description':'Run one host-bound legacy Poll-without-Job create or query phase.', 'inputSchema':poll_schema},{'name':'external.mcp_request','description':'Run one host-bound MCP list or call phase.', 'inputSchema':mcp_schema}]}
                elif initialized and method == 'tools/call':
                    params = object_of(request.get('params'), 'external_arguments_invalid')
                    context = object_of(params.get('_meta', {}).get('wechat/context'), 'host_context_required')
                    if not all(isinstance(context.get(k), str) and context[k] for k in ('account','thread','requester','purpose','request_key')): raise ValueError('host_context_required')
                    if params.get('name') == 'external.http_request': result = execute(params.get('arguments'), self.host)
                    elif params.get('name') == 'external.job_request': result = job_execute(params.get('arguments'), self.host)
                    elif params.get('name') == 'external.poll_request': result = poll_execute(params.get('arguments'), self.host)
                    elif params.get('name') == 'external.mcp_request': result = mcp_execute(params.get('arguments'), lambda x:self.mcp_host(x))
                    else: raise ValueError('method_unavailable')
                else: raise ValueError('method_unavailable')
            except (ValueError, TypeError, KeyError) as error:
                code = str(error); result = {'ok':False,'error':code if re.fullmatch(r'[a-z_]{1,96}', code) else 'external_request_invalid'}
            self.write({'jsonrpc':'2.0','id':request['id'],'result':result})

if __name__ == '__main__':
    sys.stdin.reconfigure(encoding='utf8'); sys.stdout.reconfigure(encoding='utf8'); Wire(sys.stdin, sys.stdout).serve()
