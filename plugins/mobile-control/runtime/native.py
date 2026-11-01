"""Single-process native protocol; physical I/O is a source-bound host service.

No ADB executable, host path, socket, subprocess or raw media is available here.
The packaged bridge owns high-level parsing and sequencing, while the host
independently binds every fixed I/O step to the original authorized request.
"""
import copy
import contextlib
import hashlib
import importlib.util
import json
from pathlib import Path
import re
import time
import uuid

_spec = importlib.util.spec_from_file_location('native_bridge_logic', Path(__file__).with_name('bridge.py'))
logic = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(logic)


class NativeBridge(logic.Bridge):
    def __init__(self, profile, host):
        scope = profile.get('scope')
        if not isinstance(scope, str) or not re.fullmatch('[a-f0-9]{64}', scope):
            raise logic.BridgeError('device_profile_invalid')
        self.config_object = copy.deepcopy(profile['config'])
        self.config_path = None
        self.storage = Path.cwd() / 'native-ledger' / scope
        self.storage.mkdir(parents=True, exist_ok=True)
        self.host = host
        self.previous = profile.get('previous')
        observation = profile.get('observation')
        if isinstance(observation, dict) and re.fullmatch(r'obs-[a-f0-9]{32}', observation.get('id', '')):
            logic.atomic(self.storage / (observation['id'] + '.json'), json.dumps(observation).encode('utf8'))

    def lease(self, key):
        # The host holds the physical lease for this entire authorized call.
        return contextlib.nullcontext()

    def adb(self, *args, **kwargs):
        raise logic.BridgeError('device_direct_io_forbidden')

    def foreground(self, cfg, device):
        return self.host('foreground', device['serial'])['package']

    def read_ui(self, cfg, device):
        try:
            return logic.ui_nodes(self.host('ui', device['serial'])['xml'].encode('utf8'))
        except (logic.BridgeError, ValueError, KeyError):
            return None

    def observe(self, key):
        cfg = self.config()
        device = self.device(cfg, key, 'observe')
        with self.lease(key):
            package = self.foreground(cfg, device)
            if package not in device['packages']:
                raise logic.BridgeError('device_foreground_not_permitted')
            shot = self.host('screenshot', key)
            if (not isinstance(shot, dict) or not re.fullmatch(r'blob-[a-f0-9]{64}', shot.get('ref', '')) or
                    not re.fullmatch('[a-f0-9]{64}', shot.get('sha256', '')) or
                    type(shot.get('bytes')) is not int or shot['bytes'] < 24 or
                    any(type(shot.get(k)) is not int or not 1 <= shot[k] <= 4096 for k in ('width', 'height'))):
                raise logic.BridgeError('device_screenshot_invalid')
            nodes = self.read_ui(cfg, device)
            node_state = 'available' if nodes is not None else 'unavailable'
            nodes = nodes or []
            if self.foreground(cfg, device) != package:
                raise logic.BridgeError('device_page_changed_during_observation')
            identity = 'obs-' + uuid.uuid4().hex
            snapshot = dict(id=identity, device_id=key, package=package, width=shot['width'], height=shot['height'],
                            allowed_packages=device['packages'], browsers=device.get('browsers'), web_hosts=device.get('web_hosts'),
                            screenshot_sha256=shot['sha256'], nodes=nodes,
                            ui_sha256=self.ui_hash(nodes) if nodes else '', node_state=node_state,
                            observed_at=time.time(), view_only=True)
            logic.atomic(self.storage / (identity + '.json'), json.dumps(snapshot).encode('utf8'))
            return {'snapshot': snapshot, 'screenshot_ref': shot}

    def list_devices(self):
        items = []
        for key, device in self.config()['devices'].items():
            if not device.get('enabled'):
                continue
            state = self.host('state', key)['state']
            items.append(dict(id=key, name=key, state=state, packages=device['packages'],
                              actions=device['actions'], browsers=device.get('browsers'), web_hosts=device.get('web_hosts')))
        return {'devices': items}

    def action(self, request):
        cfg = self.config()
        key, action = request['device_id'], request['action']
        device = self.device(cfg, key, action)
        if self.previous is not None:
            return copy.deepcopy(self.previous)
        operation_key, observation_id = request['request_key'], request['observation_id']
        if not logic.IDENTIFIER.fullmatch(operation_key) or not re.fullmatch(r'obs-[a-f0-9]{32}', observation_id):
            raise logic.BridgeError('device_observation_required')
        with self.lease(key):
            try:
                snapshot = json.loads((self.storage / (observation_id + '.json')).read_text(encoding='utf8'))
            except (OSError, ValueError):
                raise logic.BridgeError('device_observation_missing')
            if snapshot['device_id'] != key or not 0 <= time.time() - snapshot['observed_at'] <= 45:
                raise logic.BridgeError('device_observation_stale')
            package = self.foreground(cfg, device)
            if package != snapshot['package'] or package not in device['packages']:
                raise logic.BridgeError('device_page_changed_reobserve')
            shot = self.host('screenshot', key)
            if action != 'capture' and shot['sha256'] != snapshot['screenshot_sha256']:
                nodes = self.read_ui(cfg, device)
                if not nodes or not snapshot.get('ui_sha256') or self.ui_hash(nodes) != snapshot['ui_sha256']:
                    raise logic.BridgeError('device_page_changed_reobserve')
                if self.foreground(cfg, device) != package:
                    raise logic.BridgeError('device_page_changed_reobserve')
            # Reuse the packaged action grammar/bounds. URL/DNS and the exact
            # original string are independently checked by the host dispatch.
            if action != 'capture' and action != 'open_url':
                self.action_command(request, snapshot, device)
            if action == 'open_url':
                if request.get('package') not in (device.get('browsers') or []):
                    raise logic.BridgeError('device_browser_not_permitted')
                if not isinstance(request.get('url'), str):
                    raise logic.BridgeError('device_public_url_invalid')
            try:
                artifact = self.host('record', key) if action == 'capture' else None
                if action != 'capture':
                    if self.host('dispatch', key) != {'submitted': True}:
                        raise logic.BridgeError('device_action_unverified')
            except (logic.BridgeError, ValueError):
                return {'state': 'unknown', 'result': {}, 'replayed': False, 'reobserve_required': True}
            result = {'action': action, 'device_id': key, 'reobserve_required': True,
                      'instruction': 'Action submitted; obtain a NEW observation to verify the result.'}
            if artifact is not None:
                result['artifact'] = artifact
            return {'state': 'submitted', 'result': result, 'replayed': False}

    def read_artifact(self, request):
        self.device(self.config(), request['device_id'], 'capture')
        return self.host('artifact_read', request['device_id'])


def invoke(operation, arguments, host, validate):
    source, request = validate(operation, arguments)

    def io(step, device=''):
        reply = host({'operation': operation, 'source_version': source, 'device_id': device,
                      'step': step, 'parameters': {}})
        if not isinstance(reply, dict):
            raise logic.BridgeError('device_io_reply_invalid')
        return reply

    bridge = NativeBridge(io('profile'), io)
    if operation == 'device.list':
        result = bridge.list_devices()
    elif operation == 'device.observe':
        result = bridge.observe(request['device_id'])
    elif operation in ('device.act', 'device.record'):
        result = bridge.action(request)
    else:
        result = bridge.read_artifact(request)
    return {'operation': operation, 'source_version': source, 'result': result}
