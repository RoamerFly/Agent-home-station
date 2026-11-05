"""Pure durable device queue and workflow transitions.

The host supplies immutable identities, UTC time and verified operation facts.
This module performs no I/O, model calls, sleeps or background callbacks.
"""
import copy
from datetime import datetime, timedelta
import re

ACTIVE = {'queued', 'running'}
TERMINAL = {'completed', 'cancelled', 'failed', 'stopped', 'needs_review'}
ACTIONS = {'launch', 'tap', 'swipe', 'text', 'back', 'home', 'capture', 'open_url'}
ID = re.compile(r'[a-zA-Z0-9_-]{1,128}\Z')


def fail(code):
    raise ValueError(code)


def timestamp(value):
    if not isinstance(value, str):
        fail('device_workflow_time_invalid')
    try:
        result = datetime.fromisoformat(value.replace('Z', '+00:00'))
    except ValueError:
        fail('device_workflow_time_invalid')
    if result.tzinfo is None:
        fail('device_workflow_time_invalid')
    return result


def after(now, seconds):
    return (timestamp(now) + timedelta(seconds=seconds)).isoformat().replace('+00:00', 'Z')


def settings_valid(settings):
    if not isinstance(settings, dict):
        return False
    if any(type(settings.get(key)) is not int for key in ('revision', 'max_steps', 'max_seconds')):
        return False
    if not 1 <= settings['max_steps'] <= 30 or not 30 <= settings['max_seconds'] <= 900:
        return False
    devices, actions = settings.get('devices'), settings.get('actions')
    return (type(settings.get('enabled')) is bool and isinstance(devices, list) and len(devices) <= 16 and
            all(isinstance(value, str) and ID.fullmatch(value) for value in devices) and
            isinstance(actions, list) and len(actions) <= 9 and all(value in ACTIONS for value in actions))


def event(task, now, state, action='', summary='', reason='', observation=''):
    task['state'], task['reason'], task['updated'] = state, reason, now
    if observation:
        task['observation_id'] = observation
    summary = summary.encode('utf8')[:2400].decode('utf8', errors='ignore')
    if state == 'completed' or state == 'needs_review' and summary:
        task['result'] = summary
    task['events'] = (task.get('events', []) + [{'at':now, 'state':state, **({'action':action} if action else {}), 'summary':summary}])[-60:]
    if action:
        task['steps'] = task.get('steps', 0) + 1


def effect(task, metadata, capability, arguments):
    serial = metadata.get('serial', 0) + 1
    metadata['serial'] = serial
    metadata['effect'] = {'id':task['id'] + '-effect-' + str(serial), 'capability':capability, 'arguments':arguments}


def finish_iteration(metadata, now, delay):
    metadata['iteration'] = metadata.get('iteration', 0) + 1
    metadata['phase'] = 'observe'
    metadata['next_at'] = after(now, delay)
    metadata.pop('step', None)
    metadata.pop('observation', None)
    metadata.pop('fresh', None)


def advance(task, metadata, settings, now, fact):
    if task['state'] not in ACTIVE:
        metadata.pop('effect', None)
        return
    if not metadata.get('started'):
        metadata['started'], metadata['deadline'] = now, after(now, settings['max_seconds'])
    if timestamp(now) >= timestamp(metadata['deadline']) or metadata.get('iteration', 0) >= settings['max_steps']:
        event(task, now, 'needs_review', reason='device_step_or_time_budget')
        metadata.pop('effect', None)
        return
    if fact:
        previous = metadata.get('effect')
        if not previous or fact.get('effect_id') != previous['id']:
            fail('device_workflow_fact_changed')
        metadata.pop('effect', None)
        if fact.get('error'):
            reason = fact['error']
            if not isinstance(reason, str) or not re.fullmatch(r'device_[a-z_]{1,90}', reason):
                reason = 'device_operation_unavailable'
            event(task, now, 'needs_review', reason=reason)
            return
        value = fact.get('value')
        if not isinstance(value, dict):
            fail('device_workflow_fact_invalid')
        phase = metadata.get('phase', 'observe')
        if phase == 'observe':
            metadata['observation'] = copy.deepcopy(value)
            metadata['phase'] = 'plan'
            event(task, now, 'running', observation=value['id'])
        elif phase == 'plan':
            step = value['step']
            action = step['action']
            if action == 'finish':
                event(task, now, 'completed', summary=step.get('summary', ''), observation=task.get('observation_id', ''))
                return
            if action == 'blocked':
                event(task, now, 'needs_review', summary=step.get('summary', ''), reason='device_user_action_required')
                return
            if action == 'wait':
                finish_iteration(metadata, now, 2)
                return
            if action not in settings['actions']:
                event(task, now, 'stopped', reason='device_action_not_permitted')
                return
            metadata['step'], metadata['phase'] = copy.deepcopy(step), 'fresh'
        elif phase == 'fresh':
            old, step = metadata['observation'], metadata['step']
            same = all(value.get(key) == old.get(key) for key in ('package', 'width', 'height'))
            same = same and (step['action'] == 'capture' or value.get('png_sha256') == old.get('png_sha256') or bool(old.get('nodes')) and value.get('nodes') == old.get('nodes'))
            if not same:
                event(task, now, 'running', summary='页面已变化，重新交给模型观察', observation=value['id'])
                finish_iteration(metadata, now, 0)
                return
            metadata['fresh'], metadata['phase'] = copy.deepcopy(value), 'act'
            event(task, now, 'running', observation=value['id'])
        elif phase == 'act':
            if value.get('state') != 'submitted':
                event(task, now, 'needs_review', reason='device_action_outcome_unknown')
                return
            if metadata['step']['action'] == 'capture':
                artifact = value.get('artifact')
                if not isinstance(artifact, dict):
                    event(task, now, 'needs_review', reason='device_recording_unavailable')
                    return
                metadata['artifact'], metadata['offset'], metadata['phase'] = copy.deepcopy(artifact), 0, 'read'
            else:
                finish_iteration(metadata, now, 1)
                return
        elif phase == 'read':
            artifact = metadata['artifact']
            if value.get('id') != artifact['id'] or value.get('offset') != metadata.get('offset', 0) or value.get('sha256') != artifact['sha256'] or type(value.get('next_offset')) is not int or value['next_offset'] <= metadata.get('offset', 0) or value['next_offset'] > artifact['bytes']:
                fail('device_workflow_artifact_fact_invalid')
            metadata['offset'] = value['next_offset']
            if value.get('complete'):
                if metadata['offset'] != artifact['bytes']:
                    fail('device_workflow_artifact_fact_invalid')
                metadata['phase'] = 'analyze'
        elif phase == 'analyze':
            artifact = copy.deepcopy(metadata['artifact'])
            artifact['summary'], artifact['asset_id'] = value.get('summary', ''), value.get('asset_id', '')
            if len(task.get('artifacts', [])) >= 10:
                event(task, now, 'needs_review', reason='device_recording_limit')
                return
            task['artifacts'] = task.get('artifacts', []) + [artifact]
            if value.get('analysis_failed'):
                event(task, now, 'needs_review', reason='device_recording_unavailable')
                return
            finish_iteration(metadata, now, 1)
            return
        else:
            fail('device_workflow_phase_invalid')
    if task['state'] not in ACTIVE or metadata.get('effect'):
        return
    if metadata.get('next_at') and timestamp(now) < timestamp(metadata['next_at']):
        return
    phase = metadata.setdefault('phase', 'observe')
    if phase in ('observe', 'fresh'):
        task['state'] = 'running'
        effect(task, metadata, 'device.observe', {'device_id':task['device_id']})
    elif phase == 'plan':
        effect(task, metadata, 'device.plan', {'observation_id':metadata['observation']['id']})
    elif phase == 'act':
        step = metadata['step']
        arguments = copy.deepcopy(step.get('arguments', {}))
        arguments.update(device_id=task['device_id'], observation_id=metadata['fresh']['id'], request_key=task['id'] + '-step-' + format(metadata.get('iteration', 0), '02x'), action=step['action'])
        event(task, now, 'running', action=step['action'], summary='动作提交前检查点', observation=metadata['fresh']['id'])
        effect(task, metadata, 'device.record' if step['action'] == 'capture' else 'device.act', arguments)
    elif phase == 'read':
        artifact = metadata['artifact']
        effect(task, metadata, 'device.read_artifact', {'device_id':task['device_id'], 'artifact_id':artifact['id'], 'request_key':artifact['request_key'], 'offset':metadata.get('offset', 0)})
    elif phase == 'analyze':
        effect(task, metadata, 'artifact.analyze', {'artifact_id':metadata['artifact']['id']})
    else:
        fail('device_workflow_phase_invalid')


def transition(request):
    if not isinstance(request, dict) or set(request) != {'op', 'snapshot', 'input', 'now', 'nonce', 'facts'}:
        fail('device_workflow_request_invalid')
    snapshot = copy.deepcopy(request['snapshot'])
    now, operation, supplied = request['now'], request['op'], request['input']
    timestamp(now)
    if not isinstance(request['nonce'], str) or not request['nonce'] or not isinstance(supplied, dict):
        fail('device_workflow_request_invalid')
    settings, tasks, workflows = snapshot['settings'], snapshot['tasks'], snapshot.get('workflows', {})
    if not settings_valid(settings) or not isinstance(tasks, dict) or not isinstance(workflows, dict):
        fail('device_workflow_state_invalid')
    changed, metadata, result = {}, {}, {}
    if operation == 'save':
        proposed = supplied['settings']
        if not settings_valid(proposed) or proposed['revision'] != settings['revision']:
            fail('device_configuration_changed')
        settings = copy.deepcopy(proposed)
        settings['revision'] += 1
        for identifier, task in tasks.items():
            if task['state'] in ACTIVE:
                task['state'], task['reason'], task['updated'] = 'cancelled', 'device_permission_changed', now
                changed[identifier] = task
                metadata[identifier] = {}
    elif operation == 'start':
        if not settings['enabled'] or supplied['device_id'] not in settings['devices']:
            fail('device_task_not_permitted')
        for task in tasks.values():
            if task['request_hash'] == supplied['request_hash']:
                return {'tasks':{}, 'workflows':{}, 'result':task}
        if len(tasks) >= 200:
            fail('device_task_storage_limit')
        if sum(task['device_id'] == supplied['device_id'] and task['state'] in ACTIVE for task in tasks.values()) >= 8:
            fail('device_queue_limit')
        if not ID.fullmatch(supplied.get('id', '')) or not isinstance(supplied.get('goal'), str) or not 0 < len(supplied['goal'].encode('utf8')) <= 3000:
            fail('device_task_input_limit')
        fish_id, binding_id = supplied.get('fish_id', ''), supplied.get('device_binding_id', '')
        if not isinstance(fish_id, str) or not isinstance(binding_id, str) or bool(fish_id) != bool(binding_id):
            fail('device_task_identity_invalid')
        task = {'id':supplied['id'], 'device_id':supplied['device_id'], 'goal':supplied['goal'], 'source':supplied['source'], 'request_hash':supplied['request_hash'], 'state':'queued', 'created':now, 'updated':now, 'steps':0, 'events':[]}
        if fish_id:
            # Host-generated immutable identity fields are preserved verbatim.
            # The host policy validates their real format and current binding.
            task['fish_id'], task['device_binding_id'] = fish_id, binding_id
        changed[task['id']], metadata[task['id']], result = task, {'phase':'observe', 'iteration':0}, task
    elif operation in ('cancel', 'stop', 'recover'):
        for identifier, task in tasks.items():
            if operation == 'cancel' and identifier != supplied.get('task_id'):
                continue
            if operation == 'recover' and task['state'] in ACTIVE:
                event(task, now, 'needs_review', reason='device_worker_interrupted_reobserve_required')
            elif operation in ('cancel', 'stop') and task['state'] not in ('completed', 'cancelled'):
                task['state'], task['reason'], task['updated'] = 'cancelled', 'device_cancelled_by_owner', now
            else:
                continue
            changed[identifier], metadata[identifier] = task, {}
    elif operation == 'advance':
        identifier = supplied.get('task_id', '')
        if not identifier:
            running_devices = {task['device_id'] for task in tasks.values() if task['state'] == 'running'}
            candidates = [task for task in tasks.values() if (task['state'] == 'running' or task['state'] == 'queued' and task['device_id'] not in running_devices) and not workflows.get(task['id'], {}).get('effect')]
            candidates = [task for task in candidates if not workflows.get(task['id'], {}).get('next_at') or timestamp(now) >= timestamp(workflows[task['id']]['next_at'])]
            candidates.sort(key=lambda task:(timestamp(task['created']), task['id']))
            identifier = candidates[0]['id'] if candidates else ''
        if identifier:
            task = tasks.get(identifier)
            if task is None:
                fail('device_task_not_found')
            current = copy.deepcopy(workflows.get(identifier, {'phase':'observe', 'iteration':0}))
            advance(task, current, settings, now, request['facts'])
            changed[identifier], metadata[identifier], result = task, current, {'task_id':identifier}
    else:
        fail('device_workflow_operation_invalid')
    output = {'tasks':changed, 'workflows':metadata, 'result':result}
    if operation == 'save':
        output['settings'] = settings
    return output
