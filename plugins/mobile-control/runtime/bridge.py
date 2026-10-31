"""Private stdio MCP bridge for explicitly registered Android devices.

Only fixed ADB operations are exposed. No arbitrary shell, package install,
rooting, login, purchases, proxy changes or public listener is provided.
"""
from __future__ import annotations

import argparse
import base64
import contextlib
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import shlex
import socket
import sqlite3
import subprocess
import sys
import time
import uuid
from urllib.parse import urlsplit
import xml.etree.ElementTree as ET

SERIAL = re.compile(r"[A-Za-z0-9_.:-]{1,100}\Z")
PACKAGE = re.compile(r"[A-Za-z][A-Za-z0-9_]*(?:\.[A-Za-z][A-Za-z0-9_]*)+\Z")
IDENTIFIER = re.compile(r"[A-Za-z0-9_-]{1,128}\Z")
KEYS = {'back': '4', 'home': '3'}
ACTIONS = {'observe', 'launch', 'tap', 'swipe', 'text', 'back', 'home', 'capture', 'open_url'}
ADB_HOST = '127.0.0.1'
ADB_PORT = 5037
ADB_SERVER_VERSION = 41
ADB_PROBE_TIMEOUT_SECONDS = 1.0
ADB_PROBE_MAX_RESPONSE_BYTES = 64


def public_browser_url(value, hosts, lookup=None):
    if not isinstance(value, str) or len(value) > 8192 or not re.fullmatch(r'[A-Za-z0-9._~:/?&=%+#@,;!-]+', value):
        raise BridgeError('device_public_url_invalid')
    try:
        url = urlsplit(value)
        host = (url.hostname or '').lower()
        if url.scheme != 'https' or url.username or url.password or url.port not in (None, 443) or host not in hosts:
            raise ValueError()
        addresses = (lookup or socket.getaddrinfo)(host, 443, type=socket.SOCK_STREAM)
        if not addresses or any(not ipaddress.ip_address(item[4][0]).is_global for item in addresses):
            raise ValueError()
    except (ValueError, OSError) as exc:
        raise BridgeError('device_public_url_invalid') from exc
    return value


class BridgeError(Exception):
    pass


def _read_socket_exact(connection, length):
    data = bytearray()
    while len(data) < length:
        chunk = connection.recv(length - len(data))
        if not chunk:
            # A connected listener that stops mid-frame is a malformed ADB
            # protocol reply, not evidence that the configured endpoint may
            # be replaced with a fallback server.
            raise ValueError('adb_server_truncated_reply')
        data.extend(chunk)
    return bytes(data)


def adb_server_version_probe():
    """Read the fixed local server's ADB protocol version without spawning adb.

    `host:version` returns a four-hex-digit payload.  ADB protocol version 41
    is therefore the bytes ``b"0029"`` (0x29), not ``b"0041"``.  The bridge
    only accepts the host-owned loopback listener and never follows an
    environment-selected ADB endpoint.
    """
    try:
        with socket.create_connection((ADB_HOST, ADB_PORT), timeout=ADB_PROBE_TIMEOUT_SECONDS) as connection:
            connection.settimeout(ADB_PROBE_TIMEOUT_SECONDS)
            connection.sendall(b'000chost:version')
            if _read_socket_exact(connection, 4) != b'OKAY':
                raise ValueError()
            encoded_length = _read_socket_exact(connection, 4)
            if not re.fullmatch(rb'[0-9A-Fa-f]{4}', encoded_length):
                raise ValueError()
            length = int(encoded_length, 16)
            if not 1 <= length <= ADB_PROBE_MAX_RESPONSE_BYTES:
                raise ValueError()
            payload = _read_socket_exact(connection, length)
    except OSError as exc:
        raise BridgeError('device_adb_server_unavailable') from exc
    except ValueError as exc:
        raise BridgeError('device_adb_server_handshake_invalid') from exc
    if not re.fullmatch(rb'[0-9A-Fa-f]{4}', payload):
        raise BridgeError('device_adb_server_handshake_invalid')
    if int(payload, 16) != ADB_SERVER_VERSION:
        raise BridgeError('device_adb_server_incompatible')
    return ADB_SERVER_VERSION


def atomic(path: Path, data: bytes):
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name('.' + path.name + '-' + uuid.uuid4().hex)
    try:
        with tmp.open('xb') as f:
            if os.name != 'nt':
                os.fchmod(f.fileno(), 0o600)
            f.write(data)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, path)
    finally:
        tmp.unlink(missing_ok=True)


def ui_nodes(xml: bytes):
    if len(xml) > 1 << 20 or b'<!DOCTYPE' in xml.upper() or b'<!ENTITY' in xml.upper():
        raise BridgeError('device_ui_invalid')
    try:
        root = ET.fromstring(xml)
    except ET.ParseError as exc:
        raise BridgeError('device_ui_invalid') from exc
    result = []
    encoded_size = 2
    for i, node in enumerate(root.iter('node')):
        if i >= 3000:
            break
        bounds = re.fullmatch(r'\[(\d+),(\d+)\]\[(\d+),(\d+)\]', node.get('bounds', ''))
        if not bounds:
            continue
        item = {k: node.get(k, '')[:240] for k in ['text', 'content-desc', 'resource-id', 'class', 'package']}
        item['bounds'] = list(map(int, bounds.groups()))
        item['clickable'] = node.get('clickable') == 'true'
        item['password'] = node.get('password') == 'true'
        if item['password']:
            item['text'] = '[password field redacted]'
        if item['text'] or item['content-desc'] or item['clickable']:
            size = len(json.dumps(item, ensure_ascii=True)) + 1
            if encoded_size + size > 8 << 10:
                break
            result.append(item)
            encoded_size += size
        if len(result) >= 100:
            break
    return result


class Bridge:
    def __init__(self, config: Path, storage: Path, runner=None, server_probe=None):
        self.config_path = config.resolve()
        self.storage = storage.resolve()
        self.storage.mkdir(parents=True, exist_ok=True)
        if os.name != 'nt':
            self.storage.chmod(0o700)
        self.runner = runner or subprocess.run
        self.server_probe = server_probe or adb_server_version_probe
        self.database = self.storage / 'device-ledger.sqlite'
        with self.db() as db:
            db.execute('CREATE TABLE IF NOT EXISTS lease(device TEXT PRIMARY KEY,owner TEXT NOT NULL,expires REAL NOT NULL)')
            db.execute('CREATE TABLE IF NOT EXISTS operation(key TEXT PRIMARY KEY,hash TEXT NOT NULL,device TEXT NOT NULL,state TEXT NOT NULL,result TEXT NOT NULL,at REAL NOT NULL)')

    @contextlib.contextmanager
    def db(self):
        db = sqlite3.connect(self.database, timeout=3)
        try:
            db.execute('PRAGMA synchronous=FULL')
            with db:
                yield db
        finally:
            db.close()

    def config(self):
        try:
            data = self.config_path.read_bytes()
            if len(data) > 64 << 10:
                raise ValueError()
            cfg = json.loads(data)
            if not isinstance(cfg, dict) or not isinstance(cfg.get('devices'), dict) or len(cfg['devices']) > 16:
                raise ValueError()
            if not Path(cfg['adb']).is_absolute() or not Path(cfg['adb']).is_file():
                raise ValueError()
            # The host derives this private profile. ADB's bare/default
            # socket can auto-launch a daemon; 127.0.0.1 is deliberately a
            # remote socket spec, so a failed fixed host connection does not
            # launch one. See AOSP client/adb_client.cpp:
            # https://android.googlesource.com/platform/packages/modules/adb/+/refs/heads/main/client/adb_client.cpp
            if cfg.get('adb_host') != ADB_HOST or cfg.get('adb_port') != ADB_PORT:
                raise ValueError()
            for key, device in cfg['devices'].items():
                if not IDENTIFIER.fullmatch(key) or not SERIAL.fullmatch(device['serial']):
                    raise ValueError()
                if not isinstance(device.get('packages'), list) or len(device['packages']) > 30 or not all(PACKAGE.fullmatch(x) for x in device['packages']):
                    raise ValueError()
                if not set(device.get('actions', [])) <= ACTIONS:
                    raise ValueError()
                if not isinstance(device.get('browsers', []), list) or not set(device.get('browsers', [])) <= set(device['packages']):
                    raise ValueError()
                hosts = device.get('web_hosts', [])
                if not isinstance(hosts, list) or len(hosts) > 100 or not all(isinstance(h, str) and re.fullmatch(r'[a-z0-9.-]{1,253}', h) for h in hosts):
                    raise ValueError()
            return cfg
        except (OSError, ValueError, KeyError, TypeError) as exc:
            raise BridgeError('device_configuration_invalid') from exc

    def device(self, cfg, key, action):
        device = cfg['devices'].get(key)
        if not device or not device.get('enabled') or action not in device.get('actions', []):
            raise BridgeError('device_action_not_permitted')
        return device

    def adb(self, cfg, serial, args, timeout=12, bound=8 << 20):
        try:
            if not isinstance(args, list) or not args or args[0] in {'start-server', 'kill-server', 'server'}:
                raise BridgeError('device_command_unavailable')
            # The probe is intentionally separate from the ADB CLI: when the
            # listener is absent, malformed, or protocol-incompatible, do not
            # launch the client at all.  It cannot make the probe/client gap
            # atomic; the host owns the listener and rejects replacements.
            try:
                version = self.server_probe()
            except BridgeError:
                raise
            except Exception as exc:
                raise BridgeError('device_adb_server_handshake_invalid') from exc
            if type(version) is not int:
                raise BridgeError('device_adb_server_handshake_invalid')
            if version != ADB_SERVER_VERSION:
                raise BridgeError('device_adb_server_incompatible')
            # Do not inherit alternate ADB server selectors. The explicit
            # remote endpoint is the host-owned shared server, never a
            # session-job daemon and never a fallback to the default socket.
            environment = dict(os.environ)
            for key in ('ADB_SERVER_SOCKET', 'ANDROID_ADB_SERVER_ADDRESS', 'ANDROID_ADB_SERVER_PORT', 'ANDROID_SERIAL'):
                environment.pop(key, None)
            kwargs = dict(stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout, check=False, env=environment)
            if os.name == 'nt':
                kwargs['creationflags'] = subprocess.CREATE_NO_WINDOW
            result = self.runner([cfg['adb'], '-H', ADB_HOST, '-P', str(ADB_PORT), '-s', serial, *args], **kwargs)
            if result.returncode != 0 or len(result.stdout) > bound:
                raise BridgeError('device_command_unavailable')
            return result.stdout
        except (OSError, subprocess.TimeoutExpired) as exc:
            raise BridgeError('device_command_unavailable') from exc

    @contextlib.contextmanager
    def lease(self, key):
        owner = uuid.uuid4().hex
        with self.db() as db:
            db.execute('BEGIN IMMEDIATE')
            db.execute('DELETE FROM lease WHERE expires<?', (time.time(),))
            try:
                db.execute('INSERT INTO lease VALUES(?,?,?)', (key, owner, time.time() + 90))
            except sqlite3.IntegrityError as exc:
                raise BridgeError('device_busy') from exc
        try:
            yield
        finally:
            with self.db() as db:
                db.execute('DELETE FROM lease WHERE device=? AND owner=?', (key, owner))

    def foreground(self, cfg, device):
        data = self.adb(cfg, device['serial'], ['shell', 'dumpsys', 'window'], bound=2 << 20).decode('utf8', 'replace')
        match = re.search(r'mCurrentFocus=.*?\s([A-Za-z0-9_.]+)/[^\s}]+', data)
        if not match:
            raise BridgeError('device_foreground_unknown')
        return match.group(1)

    def read_ui(self, cfg, device):
        remote = '/sdcard/whale-ui-' + uuid.uuid4().hex + '.xml'
        try:
            self.adb(cfg, device['serial'], ['shell', 'uiautomator', 'dump', remote], timeout=15)
            return ui_nodes(self.adb(cfg, device['serial'], ['exec-out', 'cat', remote], bound=1 << 20))
        except BridgeError:
            return None
        finally:
            try:
                self.adb(cfg, device['serial'], ['shell', 'rm', '-f', remote])
            except BridgeError:
                pass

    @staticmethod
    def ui_hash(nodes):
        return hashlib.sha256(json.dumps(nodes, sort_keys=True, ensure_ascii=True).encode()).hexdigest()

    def prune_observations(self):
        # Only this bridge's generated observation files are disposable. Never
        # traverse user directories or delete the database/private configuration.
        files = sorted((p for p in self.storage.glob('obs-*.json')
                        if re.fullmatch(r'obs-[a-f0-9]{32}\.json', p.name) and not p.is_symlink()),
                       key=lambda p: p.stat().st_mtime, reverse=True)
        for i, path in enumerate(files):
            age = time.time() - path.stat().st_mtime
            if age > 120 and (i >= 200 or age > 7 * 86400):
                path.unlink(missing_ok=True)
                png = path.with_suffix('.png')
                if not png.is_symlink():
                    png.unlink(missing_ok=True)

    def observe(self, key):
        cfg = self.config()
        device = self.device(cfg, key, 'observe')
        with self.lease(key):
            package = self.foreground(cfg, device)
            if package not in device['packages']:
                raise BridgeError('device_foreground_not_permitted')
            png = self.adb(cfg, device['serial'], ['exec-out', 'screencap', '-p'])
            if not png.startswith(b'\x89PNG\r\n\x1a\n') or len(png) < 24:
                raise BridgeError('device_screenshot_invalid')
            if len(png) > 650 << 10:
                raise BridgeError('device_screenshot_exceeds_mcp_bound')
            width, height = int.from_bytes(png[16:20], 'big'), int.from_bytes(png[20:24], 'big')
            if not 1 <= width <= 4096 or not 1 <= height <= 4096:
                raise BridgeError('device_screen_dimensions_invalid')
            nodes = self.read_ui(cfg, device)
            node_state = 'available' if nodes is not None else 'unavailable'
            nodes = nodes or []
            if self.foreground(cfg, device) != package:
                raise BridgeError('device_page_changed_during_observation')
            identity = 'obs-' + uuid.uuid4().hex
            snapshot = dict(id=identity, device_id=key, package=package, width=width, height=height,
                            allowed_packages=device['packages'], browsers=device.get('browsers', []), web_hosts=device.get('web_hosts', []),
                            screenshot_sha256=hashlib.sha256(png).hexdigest(), nodes=nodes,
                            ui_sha256=self.ui_hash(nodes) if nodes else '',
                            node_state=node_state, observed_at=time.time(), view_only=True)
            atomic(self.storage / (identity + '.png'), png)
            atomic(self.storage / (identity + '.json'), json.dumps(snapshot, ensure_ascii=False).encode())
            self.prune_observations()
            return snapshot, png

    def list_devices(self):
        cfg = self.config()
        items = []
        for key, device in cfg['devices'].items():
            if not device.get('enabled'):
                continue
            state = 'offline'
            try:
                state = self.adb(cfg, device['serial'], ['get-state'], timeout=4, bound=100).decode().strip()
            except BridgeError:
                pass
            items.append(dict(id=key, name=str(device.get('name', key))[:100], state=state,
                              packages=device['packages'], actions=device.get('actions', []), browsers=device.get('browsers', []), web_hosts=device.get('web_hosts', [])))
        return dict(devices=items)

    def action(self, request):
        cfg = self.config()
        key, action = request.get('device_id'), request.get('action')
        device = self.device(cfg, key, action)
        operation_key, observation_id = request.get('request_key', ''), request.get('observation_id', '')
        if not isinstance(operation_key, str) or not IDENTIFIER.fullmatch(operation_key):
            raise BridgeError('device_request_key_required')
        if not isinstance(observation_id, str) or not re.fullmatch(r'obs-[a-f0-9]{32}', observation_id):
            raise BridgeError('device_observation_required')
        digest = hashlib.sha256(json.dumps(request, sort_keys=True).encode()).hexdigest()
        with self.lease(key):
            with self.db() as db:
                old = db.execute('SELECT hash,state,result FROM operation WHERE key=?', (operation_key,)).fetchone()
                if old:
                    if old[0] != digest:
                        raise BridgeError('device_request_key_conflict')
                    return dict(state=old[1], result=json.loads(old[2]), replayed=False)
            try:
                snapshot = json.loads((self.storage / (observation_id + '.json')).read_text(encoding='utf8'))
            except (OSError, ValueError) as exc:
                raise BridgeError('device_observation_missing') from exc
            if snapshot['device_id'] != key or not 0 <= time.time() - snapshot['observed_at'] <= 45:
                raise BridgeError('device_observation_stale')
            package = self.foreground(cfg, device)
            if package != snapshot['package'] or package not in device['packages']:
                raise BridgeError('device_page_changed_reobserve')
            # Recheck the page, not only its app name. Video pixels can change
            # while the same controls remain; validate those controls before acting.
            current = self.adb(cfg, device['serial'], ['exec-out', 'screencap', '-p'])
            if action != 'capture' and hashlib.sha256(current).hexdigest() != snapshot['screenshot_sha256']:
                nodes = self.read_ui(cfg, device)
                if not nodes or not snapshot.get('ui_sha256') or self.ui_hash(nodes) != snapshot['ui_sha256']:
                    raise BridgeError('device_page_changed_reobserve')
                if self.foreground(cfg, device) != package:
                    raise BridgeError('device_page_changed_reobserve')
            command = None if action == 'capture' else self.action_command(request, snapshot, device)
            seconds = request.get('seconds', 6)
            if action == 'capture' and (type(seconds) is not int or not 2 <= seconds <= 12):
                raise BridgeError('device_capture_duration_invalid')
            with self.db() as db:
                db.execute('INSERT INTO operation VALUES(?,?,?,?,?,?)', (operation_key, digest, key, 'unknown', '{}', time.time()))
            try:
                capture = self.capture(cfg, device, key, operation_key, seconds) if action == 'capture' else None
                if command is not None:
                    self.adb(cfg, device['serial'], command, timeout=15, bound=1 << 20)
            except BridgeError:
                # A lost response does not prove that the tap did not execute.
                return dict(state='unknown', result={}, replayed=False, reobserve_required=True)
            result = dict(action=action, device_id=key, reobserve_required=True,
                          instruction='Action submitted; obtain a NEW observation to verify the result.')
            if capture is not None:
                result['artifact'] = capture
            with self.db() as db:
                db.execute('UPDATE operation SET state=?,result=? WHERE key=?', ('submitted', json.dumps(result), operation_key))
            return dict(state='submitted', result=result, replayed=False)

    def capture(self, cfg, device, key, operation_key, seconds):
        identity = 'clip-' + uuid.uuid4().hex
        remote = '/sdcard/whale-' + identity + '.mp4'
        try:
            self.adb(cfg, device['serial'], ['shell', 'screenrecord', '--size', '480x800', '--bit-rate', '256000', '--time-limit', str(seconds), remote], timeout=seconds + 10, bound=4096)
            data = self.adb(cfg, device['serial'], ['exec-out', 'cat', remote], bound=2 << 20)
            if len(data) < 32 or data[4:8] != b'ftyp':
                raise BridgeError('device_capture_invalid')
            if self.foreground(cfg, device) not in device['packages']:
                raise BridgeError('device_capture_app_changed')
            meta = dict(id=identity, device_id=key, request_key=operation_key, kind='screen_recording',
                        mime='video/mp4', bytes=len(data), sha256=hashlib.sha256(data).hexdigest(),
                        requested_seconds=seconds, audio=False, original_media=False, created_at=time.time())
            atomic(self.storage / (identity + '.mp4'), data)
            atomic(self.storage / (identity + '.json'), json.dumps(meta).encode())
            return meta
        finally:
            try:
                self.adb(cfg, device['serial'], ['shell', 'rm', '-f', remote])
            except BridgeError:
                pass

    def read_artifact(self, request):
        cfg = self.config()
        key = request.get('device_id')
        self.device(cfg, key, 'capture')
        identity = request.get('artifact_id', '')
        offset = request.get('offset', 0)
        if not isinstance(identity, str) or not re.fullmatch(r'clip-[a-f0-9]{32}', identity) or type(offset) is not int or not 0 <= offset <= 2 << 20:
            raise BridgeError('device_artifact_invalid')
        try:
            meta_path, path = self.storage / (identity + '.json'), self.storage / (identity + '.mp4')
            if meta_path.is_symlink() or path.is_symlink():
                raise ValueError()
            meta = json.loads(meta_path.read_text(encoding='utf8'))
            if meta['device_id'] != key or meta['request_key'] != request.get('request_key') or not 0 <= time.time() - meta['created_at'] <= 7 * 86400 or path.stat().st_size != meta['bytes']:
                raise ValueError()
            with path.open('rb') as f:
                f.seek(offset)
                data = f.read(8192)
            return dict(id=identity, offset=offset, bytes=meta['bytes'], sha256=meta['sha256'],
                        data_base64=base64.b64encode(data).decode('ascii'), eof=offset + len(data) >= meta['bytes'])
        except (OSError, ValueError, KeyError, TypeError) as exc:
            raise BridgeError('device_artifact_unavailable') from exc

    @staticmethod
    def action_command(request, snapshot, device):
        action = request['action']
        def coordinate(name, extent):
            value = request.get(name)
            if type(value) is not int or not 0 <= value < extent:
                raise BridgeError('device_coordinate_invalid')
            return str(value)
        if action == 'tap':
            return ['shell', 'input', 'tap', coordinate('x', snapshot['width']), coordinate('y', snapshot['height'])]
        if action == 'swipe':
            duration = request.get('duration_ms', 350)
            if type(duration) is not int or not 100 <= duration <= 1500:
                raise BridgeError('device_swipe_duration_invalid')
            return ['shell', 'input', 'swipe', coordinate('x', snapshot['width']), coordinate('y', snapshot['height']), coordinate('x2', snapshot['width']), coordinate('y2', snapshot['height']), str(duration)]
        if action in KEYS:
            return ['shell', 'input', 'keyevent', KEYS[action]]
        if action == 'launch':
            package = request.get('package', '')
            if package not in device['packages'] or not PACKAGE.fullmatch(package):
                raise BridgeError('device_package_not_permitted')
            return ['shell', 'monkey', '-p', package, '-c', 'android.intent.category.LAUNCHER', '1']
        if action == 'open_url':
            package = request.get('package', '')
            if package not in device.get('browsers', []) or not PACKAGE.fullmatch(package):
                raise BridgeError('device_browser_not_permitted')
            url = public_browser_url(request.get('url'), device.get('web_hosts', []))
            # adb shell joins its arguments before passing them to Android's
            # shell. Quote the URL so & and ; are data, never shell operators.
            return ['shell', 'am', 'start', '-a', 'android.intent.action.VIEW', '-d', shlex.quote(url), '-p', package]
        if action == 'text':
            text = request.get('text', '')
            if not isinstance(text, str) or not re.fullmatch(r'[A-Za-z0-9 @._-]{1,200}', text):
                raise BridgeError('device_text_requires_supported_ime')
            return ['shell', 'input', 'text', text.replace(' ', '%s')]
        raise BridgeError('device_action_invalid')


def tools():
    device = {'type': 'string', 'maxLength': 128}
    return [
        dict(name='devices', description='List explicitly registered devices and permitted actions.', inputSchema={'type': 'object', 'properties': {}, 'additionalProperties': False}),
        dict(name='observe', description='Capture the current permitted app screenshot and bounded UI nodes. Does not mean watching a full video.', inputSchema={'type': 'object', 'properties': {'device_id': device}, 'required': ['device_id'], 'additionalProperties': False}),
        dict(name='act', description='Execute ONE permitted action from a fresh observation. capture records 2-12 seconds of screen WITHOUT audio, never the original video. Repeated request_key never replays an operation, including unknown outcomes. Reobserve after every action.', inputSchema={'type': 'object', 'properties': {'device_id': device, 'action': {'type': 'string', 'enum': sorted(ACTIONS - {'observe'})}, 'request_key': device, 'observation_id': device, 'package': device, 'url':{'type':'string','maxLength':8192}, 'text': {'type': 'string', 'maxLength': 200}, **{key: {'type': 'integer'} for key in ['x', 'y', 'x2', 'y2', 'duration_ms', 'seconds']}}, 'required': ['device_id', 'action', 'request_key', 'observation_id'], 'additionalProperties': False}),
        dict(name='read_artifact', description='Read a bounded chunk of a recording created by this exact device and request. No arbitrary file access.', inputSchema={'type':'object','properties':{'device_id':device,'artifact_id':device,'request_key':device,'offset':{'type':'integer','minimum':0,'maximum':2097152}},'required':['device_id','artifact_id','request_key','offset'],'additionalProperties':False}),
    ]


def serve(bridge):
    for line in sys.stdin.buffer:
        if len(line) > 64 << 10:
            continue
        request = None
        try:
            request = json.loads(line)
            if not isinstance(request, dict) or 'id' not in request:
                continue
            method, params = request.get('method'), request.get('params') or {}
            if method == 'initialize':
                result = dict(protocolVersion='2024-11-05', capabilities={'tools': {}}, serverInfo={'name': 'whale-mobile-bridge', 'version': '0.1.0'})
            elif method == 'tools/list':
                result = {'tools': tools()}
            elif method == 'tools/call':
                name, args = params.get('name'), params.get('arguments') or {}
                if name == 'devices':
                    value, png = bridge.list_devices(), None
                elif name == 'observe':
                    value, png = bridge.observe(args.get('device_id'))
                elif name == 'act':
                    value, png = bridge.action(args), None
                elif name == 'read_artifact':
                    value, png = bridge.read_artifact(args), None
                else:
                    raise BridgeError('device_tool_unknown')
                content = [{'type': 'text', 'text': json.dumps(value, ensure_ascii=False)}]
                if png:
                    content.append({'type': 'image', 'mimeType': 'image/png', 'data': base64.b64encode(png).decode('ascii')})
                result = {'content': content, 'isError': False}
            else:
                raise BridgeError('device_method_unknown')
            response = {'jsonrpc': '2.0', 'id': request['id'], 'result': result}
        except Exception as exc:
            if not isinstance(request, dict) or 'id' not in request:
                continue
            # Never forward subprocess stderr, local paths or private config.
            code = str(exc) if isinstance(exc, BridgeError) else 'device_request_invalid'
            response = {'jsonrpc': '2.0', 'id': request['id'], 'result': {'content': [{'type': 'text', 'text': code}], 'isError': True}}
        sys.stdout.write(json.dumps(response, ensure_ascii=True) + '\n')
        sys.stdout.flush()


def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--storage', type=Path, required=True)
    args = parser.parse_args(argv)
    serve(Bridge(args.config, args.storage))


if __name__ == '__main__':
    main()
