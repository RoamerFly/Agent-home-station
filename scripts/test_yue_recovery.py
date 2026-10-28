"""Synthetic YuE recovery: real broker HTTP/restarts, no model or WeChat sends."""
import base64
import concurrent.futures
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request
import wave
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from process_window import no_console_options
import rvc_recovery
from yue_bridge import song_recipe
import yue_recovery as recovery
from test_yue_sampling import native_info, WORKFLOWS


class YueRecoveryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.jid = 'c' * 32
        self.body = dict(title='Synthetic', mode='original', style='folk', lyrics='[Verse]\nSynthetic',
                         duration_seconds=10, seed=42, singing_voice='native')
        self.graph = {'output': {'class_type': 'SyntheticOutput', 'inputs': {'seed': 42}}}
        self.job = dict(id=self.jid, engine='yue', state='needs_review', recipe=song_recipe(self.body),
                        graph_sha256=recovery.digest(self.graph))
        self.job['checkpoint_sha256'] = recovery.persist(self.root, self.jid, self.body, self.graph, self.root)
        self.jobs = {self.jid: self.job}
        self.calls = []
        self.history = {}
        self.queue = {'queue_running': [], 'queue_pending': []}

    def prompt(self, ident='original-prompt'):
        return [1, ident, self.graph, {'client_id': 'wechat-yue-' + self.jid,
                                     'wechat_graph_sha256': self.job['graph_sha256']}, ['output']]

    def api(self, route):
        return self.queue if route == '/queue' else self.history

    def resume(self, **overrides):
        values = dict(jobs=self.jobs, jid=self.jid, state=self.root, input_dir=self.root,
                      recipe_reader=song_recipe, profile_reader=lambda: None, api=self.api,
                      save=lambda: rvc_recovery.save_jobs(self.root / 'jobs.json', self.jobs),
                      submit=lambda *args: self.calls.append(args))
        values.update(overrides)
        return recovery.resume(**values)

    def test_running_and_completed_prompt_reattach_without_submission(self):
        self.queue['queue_running'] = [self.prompt()]
        result = self.resume()
        self.assertEqual(result['id'], self.jid)
        self.assertEqual(self.calls[0][2], 'original-prompt')
        self.assertEqual(self.calls[0][1]['body'], self.body)
        self.assertEqual(result['resume_count'], 1)
        for state in ('queued', 'running', 'completed'):
            self.job['state'] = state
            self.resume(restart_missing=True)
        self.assertEqual(len(self.calls), 1)

    def test_queued_before_submission_recovers_frozen_request_without_backend_replay(self):
        self.job.pop('checkpoint_sha256')
        self.job.pop('graph_sha256')
        self.job.update(queued_checkpoint_sha256=recovery.persist(self.root,self.jid,self.body,self.graph,self.root,prepared=False),
                        queued_graph_sha256=recovery.digest(self.graph))
        def no_backend(_):
            self.fail('unsubmitted queued request should not need backend history')
        result=self.resume(api=no_backend)
        self.assertEqual(result['phase'],'preparing_yue')
        self.assertFalse(self.calls[0][1]['prepared'])
        self.assertEqual(self.calls[0][1]['body']['seed'],42)
        self.assertIsNone(self.calls[0][2])
        self.job['state']='needs_review';self.job['submission_attempts']=1
        with self.assertRaisesRegex(ValueError,'submission_checkpoint_missing'):
            self.resume(restart_missing=True)

    def test_missing_requires_explicit_restart_and_one_concurrent_claim(self):
        with self.assertRaisesRegex(ValueError, 'explicit_restart_required'):
            self.resume()
        self.assertEqual(self.calls, [])
        self.assertEqual(self.job['state'], 'needs_review')
        lock = threading.Lock()
        def claim(_):
            with lock:
                return self.resume(restart_missing=True)
        with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
            list(pool.map(claim, range(16)))
        self.assertEqual(len(self.calls), 1)
        self.assertIsNone(self.calls[0][2])
        self.assertEqual(self.calls[0][1]['graph'], self.graph)
        self.assertIn('backend_absent_verified_at', self.job)
        self.assertEqual(len(self.jobs), 1)

    def test_history_second_read_closes_completion_race(self):
        reads = []
        def api(route):
            reads.append(route)
            if route == '/queue':
                return self.queue
            return {} if len(reads) == 1 else {'original-prompt': {'prompt': self.prompt()}}
        self.assertEqual(self.resume(api=api)['prompt_id'], 'original-prompt')
        self.assertEqual(reads, ['/history', '/queue', '/history'])

    def test_changed_checkpoint_recipe_reference_and_voice_rejected(self):
        path = recovery.checkpoint_path(self.root, self.jid)
        saved = path.read_bytes()
        path.write_bytes(saved + b' ')
        with self.assertRaisesRegex(ValueError, 'checkpoint_changed'):
            self.resume(restart_missing=True)
        path.write_bytes(saved)
        self.job['recipe']['seed'] = 99
        with self.assertRaisesRegex(ValueError, 'recipe_changed'):
            self.resume(restart_missing=True)
        self.job['recipe'] = song_recipe(self.body)
        self.job['singing_voice'] = {'profile': 'C', 'engine': 'rvc', 'config_sha256': 'pinned'}
        with self.assertRaisesRegex(ValueError, 'voice_configuration_changed'):
            self.resume(restart_missing=True)
        self.job.pop('singing_voice')
        body = dict(self.body, mode='cover', reference_base64=base64.b64encode(b'synthetic source').decode())
        reference = self.root / ('wechat-ref-' + self.jid + '.wav')
        reference.write_bytes(b'synthetic prepared audio')
        self.job.update(recipe=song_recipe(body), checkpoint_sha256=recovery.persist(self.root, self.jid, body, self.graph, self.root))
        reference.write_bytes(b'changed audio')
        with self.assertRaisesRegex(ValueError, 'reference_changed'):
            self.resume(restart_missing=True)
        self.assertEqual(self.calls, [])

    def test_unknown_backend_ambiguous_prompt_and_cancelled_never_restart(self):
        def offline(_):
            raise OSError('synthetic unreachable backend')
        with self.assertRaises(OSError):
            self.resume(api=offline, restart_missing=True)
        self.history = {'one': {'prompt': self.prompt('one')}, 'two': {'prompt': self.prompt('two')}}
        with self.assertRaisesRegex(ValueError, 'multiple_prompts'):
            self.resume(restart_missing=True)
        self.job['prompt_id'] = 'expected'
        with self.assertRaisesRegex(ValueError, 'identity_mismatch'):
            self.resume(restart_missing=True)
        self.job['state'] = 'cancelled'
        with self.assertRaisesRegex(ValueError, 'not_resumable'):
            self.resume(restart_missing=True)
        self.assertEqual(self.calls, [])

    def test_failure_to_save_or_schedule_retains_recoverable_identity(self):
        self.queue['queue_pending'] = [self.prompt()]
        before = dict(self.job)
        def fail():
            raise OSError('synthetic full disk')
        with self.assertRaises(OSError):
            self.resume(save=fail)
        self.assertEqual(self.job, before)
        def cannot_schedule(*args):
            raise RuntimeError('synthetic executor stopped')
        with self.assertRaises(RuntimeError):
            self.resume(submit=cannot_schedule)
        self.assertEqual(self.job['state'], 'needs_review')
        self.assertEqual(self.job['prompt_id'], 'original-prompt')
        self.assertEqual(self.calls, [])

    @unittest.skipUnless(shutil.which('ffprobe'), 'real generated-fixture duration validation')
    def test_real_http_broker_restart_lost_response_and_frozen_recompute(self):
        audio = io.BytesIO()
        with wave.open(audio, 'wb') as wav:
            wav.setnchannels(1); wav.setsampwidth(2); wav.setframerate(8000)
            wav.writeframes(b'\0\0' * 80000)
        payload = audio.getvalue()
        backend_state = {'prompts': {}, 'posts': [], 'release': False, 'drop': False}
        class Backend(BaseHTTPRequestHandler):
            def log_message(self, *unused): pass
            def reply(self, value):
                data = json.dumps(value).encode(); self.send_response(200)
                self.send_header('Content-Length', str(len(data))); self.end_headers(); self.wfile.write(data)
            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers.get('Content-Length', '0'))))
                if self.path == '/prompt':
                    backend_state['posts'].append(body)
                    pid = 'prompt-' + str(len(backend_state['posts']))
                    extra = dict(body.get('extra_data', {}), client_id=body['client_id'])
                    backend_state['prompts'][pid] = [1, pid, body['prompt'], extra, ['10']]
                    if backend_state['drop']:
                        backend_state['drop'] = False
                        self.connection.shutdown(socket.SHUT_RDWR)
                        return
                    return self.reply({'prompt_id': pid})
                return self.reply({})
            def do_GET(self):
                if self.path == '/queue':
                    return self.reply({'queue_running': [] if backend_state['release'] else list(backend_state['prompts'].values()), 'queue_pending': []})
                if self.path.startswith('/history'):
                    history = {pid: {'prompt': item, 'status': {'status_str': 'success'},
                                     'outputs': {'10': {'audio': [{'filename': 'synthetic.wav', 'type': 'output', 'subfolder': ''}]}}}
                               for pid, item in backend_state['prompts'].items()} if backend_state['release'] else {}
                    if self.path != '/history':
                        pid = self.path.split('/')[-1]; history = {pid: history[pid]} if pid in history else {}
                    return self.reply(history)
                if self.path.startswith('/view?'):
                    self.send_response(200); self.send_header('Content-Length', str(len(payload))); self.end_headers(); self.wfile.write(payload); return
                if self.path.startswith('/object_info/'):
                    kind = self.path.split('/')[-1]
                    return self.reply({kind: native_info()[kind]})
                return self.reply({})

        backend = ThreadingHTTPServer(('127.0.0.1', 0), Backend)
        thread = threading.Thread(target=backend.serve_forever, daemon=True); thread.start()
        self.addCleanup(thread.join); self.addCleanup(backend.server_close); self.addCleanup(backend.shutdown)
        token = self.root / 'token'; token.write_text('synthetic-token')
        state = self.root / 'broker'; state.mkdir()
        bootstrap = '''
import sys,urllib.request
import yue_bridge
original=urllib.request.urlopen
port=sys.argv.pop()
def backend(request,timeout=20):
    if isinstance(request,str):request=urllib.request.Request(request)
    request.full_url=request.full_url.replace('127.0.0.1:8188','127.0.0.1:'+port)
    return original(request,timeout=timeout)
yue_bridge.urllib.request.urlopen=backend
yue_bridge.main()
'''
        children = []
        self.addCleanup(lambda: [stop(p) for p in children])
        env = dict(os.environ, PYTHONPATH=str(Path(__file__).parent.resolve()), PYTHONDONTWRITEBYTECODE='1')
        def start():
            with socket.socket() as sock:
                sock.bind(('127.0.0.1', 0)); port = sock.getsockname()[1]
            log = (self.root / ('broker-' + str(len(children)) + '.log')).open('wb')
            self.addCleanup(log.close)
            child = subprocess.Popen([sys.executable, '-c', bootstrap, '--project', str(self.root), '--workflow-dir', str(WORKFLOWS),
                                      '--state', str(state), '--token-file', str(token), '--port', str(port), str(backend.server_port)],
                                     env=env, stdout=log, stderr=log, **no_console_options())
            children.append(child)
            base = 'http://127.0.0.1:' + str(port)
            wait(lambda: request(base, '/health')[0] == 200, child)
            return child, base
        client = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        def request(base, route, body=None):
            headers = {'Authorization': 'Bearer synthetic-token', 'Idempotency-Key': 'same-original-song'}
            req = urllib.request.Request(base + route, data=None if body is None else json.dumps(body).encode(), headers=headers)
            try:
                with client.open(req, timeout=3) as reply: return reply.status, json.load(reply)
            except urllib.error.HTTPError as error: return error.code, json.load(error)
        def wait(check, child):
            deadline = time.monotonic() + 15
            while time.monotonic() < deadline:
                if child.poll() is not None: self.fail('synthetic broker stopped; inspect fixture log')
                try:
                    if check(): return
                except (OSError, ValueError): pass
                time.sleep(.04)
            self.fail('synthetic broker observation timeout')
        def stop(child):
            if child.poll() is None:
                child.terminate()
                try: child.wait(timeout=5)
                except subprocess.TimeoutExpired: child.kill(); child.wait()
        child, base = start()
        code, created = request(base, '/jobs', self.body)
        self.assertEqual(code, 202, created)
        jid = created['id']; route = '/jobs/' + jid
        wait(lambda: request(base, route)[1].get('prompt_id') == 'prompt-1', child)
        stop(child)
        child, base = start()
        wait(lambda: request(base, route)[1].get('resume_count') == 1, child)
        self.assertEqual(len(backend_state['posts']), 1)
        backend_state['release'] = True
        wait(lambda: request(base, route)[1].get('state') == 'completed', child)
        self.assertEqual((state / (jid + '.wav')).read_bytes(), payload)
        self.assertEqual(request(base, route + '/resume', {})[1]['state'], 'completed')
        self.assertEqual(request(base, '/jobs', self.body)[1]['id'], jid)
        self.assertEqual(len(json.loads((state / 'jobs.json').read_text())), 1)
        stop(child)
        # Simulate lost backend state after bridge restart, while retaining the
        # frozen checkpoint. Default recovery must not make another prompt.
        jobs = json.loads((state / 'jobs.json').read_text()); jobs[jid]['state'] = 'running'
        rvc_recovery.save_jobs(state / 'jobs.json', jobs)
        backend_state.update(prompts={}, release=False, drop=True)
        child, base = start()
        self.assertEqual(request(base, route)[1]['state'], 'needs_review')
        self.assertEqual(request(base, route + '/resume', {})[0], 409)
        self.assertEqual(len(backend_state['posts']), 1)
        self.assertEqual(request(base, route + '/resume', {'restart_missing': True})[0], 200)
        wait(lambda: request(base, route)[1]['state'] == 'needs_review', child)
        self.assertEqual(len(backend_state['posts']), 2)
        self.assertEqual(backend_state['posts'][0], backend_state['posts'][1])
        # The second response was lost after backend acceptance. Reattach via
        # client/graph identity, not by submitting a third computation.
        self.assertEqual(request(base, route + '/resume', {})[0], 200)
        wait(lambda: request(base, route)[1].get('prompt_id') == 'prompt-2', child)
        self.assertEqual(len(backend_state['posts']), 2)
        backend_state['release'] = True
        wait(lambda: request(base, route)[1]['state'] == 'completed', child)
        self.assertEqual(len(json.loads((state / 'jobs.json').read_text())), 1)
        stop(child)


if __name__ == '__main__': unittest.main()
