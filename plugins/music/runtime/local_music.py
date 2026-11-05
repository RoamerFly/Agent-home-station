"""Local music service protocol. Bodies, credentials and transport stay in host."""
import json
from pathlib import Path
import re


def _load_templates():
    document = json.loads(Path(__file__).with_name('error_templates.json').read_text(encoding='utf8'))
    if not isinstance(document, dict) or set(document) != {'version', 'templates'} or type(document['version']) is not int or document['version'] != 1 or not isinstance(document['templates'], dict) or not document['templates']:
        raise ValueError('invalid_music_error_templates')
    for code, template in document['templates'].items():
        if (not isinstance(code, str) or not re.fullmatch('[a-z][a-z0-9_]{0,159}', code) or
                not isinstance(template, dict) or set(template) != {'kind', 'message'} or
                template['kind'] not in ('service', 'input') or
                not isinstance(template['message'], str) or not template['message'] or
                any(char in template['message'].replace('{provider_code}', '') for char in '{}')):
            raise ValueError('invalid_music_error_templates')
    return document['templates']


ERROR_TEMPLATES = _load_templates()


def _normalized(code, provider):
    template = ERROR_TEMPLATES[code]
    out = {'kind': template['kind'], 'code': code,
           'message': template['message'].replace('{provider_code}', provider or '')}
    if provider is not None:
        out['provider_code'] = provider
    return out


def _ref(value, name, minimum):
    if (not isinstance(value, dict) or set(value) != {'ref', 'sha256', 'bytes'} or
            value['ref'] != name or not isinstance(value['sha256'], str) or
            not re.fullmatch('[a-f0-9]{64}', value['sha256']) or
            type(value['bytes']) is not int or value['bytes'] < minimum):
        raise ValueError('invalid_music_bridge_body_ref')


def validate_request(args):
    if not isinstance(args, dict) or set(args) - {'method', 'path', 'body_ref', 'idempotency_key'} or not {'method', 'path'} <= set(args):
        raise ValueError('invalid_music_bridge_arguments')
    method, path = args['method'], args['path']
    if not isinstance(path, str) or method not in ('GET', 'POST'):
        raise ValueError('music_bridge_route_denied')
    get = method == 'GET' and path in ('/health', '/rvc-health', '/parameters', '/sampling-options')
    start = method == 'POST' and path in ('/jobs', '/rvc-jobs')
    job = re.fullmatch(r'/jobs/([A-Za-z0-9_@-]{1,128})(?:/(audio|resume|cancel))?', path)
    action = job.group(2) if job else None
    if not (get or start or job and (method == 'GET' and action in (None, 'audio') or method == 'POST' and action in ('resume', 'cancel'))):
        raise ValueError('music_bridge_route_denied')
    if 'body_ref' in args:
        if method != 'POST':
            raise ValueError('invalid_music_bridge_body_ref')
        _ref(args['body_ref'], 'request_0', 1)
    elif start or action == 'resume':
        raise ValueError('music_bridge_body_required')
    if 'idempotency_key' in args:
        key = args['idempotency_key']
        if method != 'POST' or not isinstance(key, str) or not key or any(ord(char) < 33 or ord(char) > 126 for char in key):
            raise ValueError('invalid_music_bridge_idempotency_key')
    elif start:
        raise ValueError('music_bridge_idempotency_key_required')
    return args


def _provider_code(reply):
    value = reply.get('json')
    code = value.get('error') if isinstance(value, dict) else None
    # Never interpolate arbitrary server diagnostics, paths or credentials.
    if not isinstance(code, str) or len(code) > 160:
        return None
    if re.fullmatch(r'(?:[a-z][a-z0-9_]*|sampling_[a-z0-9_]+:[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)*)', code):
        return code
    return None


def normalize_error(reply):
    """Classify actual HTTP/code evidence; never retry or submit a new job."""
    status, provider = reply['status'], _provider_code(reply)
    if 200 <= status <= 299:
        return None
    if status == 409:
        if provider == 'music_account_scope_mismatch':
            return _normalized('yue_account_scope_mismatch', provider)
        if provider and provider.startswith('rvc_recovery_'):
            return _normalized('rvc_recovery_failed', provider)
        if provider == 'yue_recovery_prompt_missing_explicit_restart_required':
            return _normalized('yue_recovery_restart_missing_required', provider)
        if provider == 'yue_recovery_prompt_failed_explicit_retry_required':
            return _normalized('yue_recovery_retry_failed_required', provider)
        if provider and provider.startswith('yue_recovery_'):
            return _normalized('yue_recovery_failed', provider)
    if status == 400 and provider and (provider.startswith(('rvc_', 'sampling_', 'invalid_sampling_')) or provider in ('invalid_planning_mode', 'score_sampling_requires_active_planning')):
        return _normalized('yue_native_parameters_invalid', provider)
    if status in (401, 403):
        return _normalized('yue_bridge_auth_failed', provider)
    if status == 503:
        if provider and provider.startswith('rvc_'):
            return _normalized('rvc_service_not_ready', provider)
        if provider == 'music_recovery_unavailable':
            return _normalized('music_recovery_unavailable', provider)
        return _normalized('yue_backend_unavailable', provider)
    if status == 404:
        return _normalized('yue_job_not_found', provider)
    return _normalized('yue_request_rejected', provider)


def bridge_request(args, request):
    validate_request(args)
    reply = request(args)
    if (not isinstance(reply, dict) or set(reply) - {'status', 'mime', 'body_ref', 'json'} or
            not {'status', 'mime', 'body_ref'} <= set(reply) or
            type(reply['status']) is not int or not 100 <= reply['status'] <= 599 or
            not isinstance(reply['mime'], str) or len(reply['mime']) > 512 or
            any(ord(char) < 32 or ord(char) == 127 for char in reply['mime'])):
        raise ValueError('invalid_music_bridge_reply')
    _ref(reply['body_ref'], 'reply_0', 0)
    result = dict(reply)
    error = normalize_error(reply)
    if error is not None:
        result['normalized_error'] = error
    return result
