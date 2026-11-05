"""Durable computation recovery; synthetic recordings, no models or sends."""
import base64
import concurrent.futures
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
import unittest
from unittest.mock import patch
import rvc_cover
import rvc_recovery as recovery
from process_window import no_console_options


class RecoveryTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.jid = 'a'*32
        self.body = dict(title='Synthetic', reference_base64=base64.b64encode(b'complete source').decode(),
                         reference_seconds=90, reference_start_seconds=0, reference_pretrimmed=True)
        self.profile = dict(profile='C', engine='rvc', config_sha256='pinned')
        self.job = dict(id=self.jid, state='needs_review', engine='rvc', recipe=rvc_cover.recipe(self.body),
                        singing_voice=self.profile.copy())
        self.jobs = {self.jid: self.job}
        self.calls = []
        self.db = self.root/'jobs.json'
        recovery.persist_reference(self.root, self.jid, self.body)

    def resume(self, profile=None, save=None, submit=None):
        return recovery.resume(self.jobs, self.jid, self.root, 'config', lambda _: profile or self.profile,
                               save or (lambda: recovery.save_jobs(self.db, self.jobs)),
                               submit or (lambda jid, body: self.calls.append((jid, body))))

    def test_original_identity_source_and_recipe_survive_restart(self):
        result = self.resume()
        self.assertEqual(result['id'], self.jid)
        self.assertEqual(result['resume_count'], 1)
        self.assertEqual(self.calls, [(self.jid, dict(self.body, reference_source_sha256=None))])
        persisted = json.loads(self.db.read_text())
        self.assertEqual(persisted[self.jid]['state'], 'queued')
        self.assertNotIn('reference_base64', persisted[self.jid]['recipe'])
        self.assertEqual(len(persisted), 1)

    def test_concurrent_repeated_resumes_schedule_one_compute(self):
        lock = threading.RLock()
        def run(_):
            with lock: return self.resume()
        with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
            list(pool.map(run, range(30)))
        self.assertEqual(len(self.calls), 1)
        for state in ('queued', 'running', 'completed'):
            self.job['state'] = state
            self.assertEqual(self.resume()['state'], state)
        self.assertEqual(len(self.calls), 1)

    def test_missing_corrupt_and_changed_recipe_do_not_schedule(self):
        source = recovery.source_path(self.root, self.jid)
        source.unlink()
        with self.assertRaisesRegex(ValueError, 'source_missing'): self.resume()
        source.write_bytes(b'partial')
        with self.assertRaisesRegex(ValueError, 'source_changed'): self.resume()
        source.unlink(); recovery.persist_reference(self.root, self.jid, self.body)
        self.job['recipe']['melody_source'] = 'changed'
        with self.assertRaisesRegex(ValueError, 'recipe_changed'): self.resume()
        self.assertEqual(self.calls, [])

    def test_voice_configuration_engine_or_cancelled_job_cannot_be_replaced(self):
        for key, value in [('profile', 'other'), ('config_sha256', 'new'), ('engine', 'seed-vc')]:
            with self.assertRaisesRegex(ValueError, 'profile_changed'):
                self.resume(profile=dict(self.profile, **{key: value}))
        for state in ('cancelled', 'unknown'):
            self.job['state'] = state
            with self.assertRaisesRegex(ValueError, 'state_not_resumable'): self.resume()
        self.job.update(state='failed', engine='yue')
        with self.assertRaisesRegex(ValueError, 'engine_required'): self.resume()
        self.assertEqual(self.calls, [])

    def test_failed_persistence_and_submission_preserve_recoverable_original(self):
        def fail(): raise OSError('synthetic disk error')
        before = dict(self.job)
        with self.assertRaises(OSError): self.resume(save=fail)
        self.assertEqual(self.job, before)
        self.assertEqual(self.calls, [])
        def cannot_submit(*_): raise RuntimeError('synthetic pool stopped')
        with self.assertRaises(RuntimeError): self.resume(submit=cannot_submit)
        self.assertEqual(self.job['state'], 'needs_review')
        self.assertEqual(recovery.recover_body(self.root, self.jid, self.job)['reference_seconds'], 90)

    def test_publication_never_replaces_conflicts_and_cleans_partial_stage(self):
        path = recovery.source_path(self.root, self.jid)
        with self.assertRaisesRegex(ValueError, 'source_changed'):
            recovery.persist_reference(self.root, self.jid, dict(self.body, reference_base64=base64.b64encode(b'other').decode()))
        self.assertEqual(path.read_bytes(), b'complete source')
        path.unlink()
        with patch('rvc_recovery.os.link', side_effect=OSError('synthetic interrupted publish')):
            with self.assertRaises(OSError): recovery.persist_reference(self.root, self.jid, self.body)
        self.assertFalse(path.exists())
        self.assertEqual(list(self.root.iterdir()), [])

    def test_paths_and_link_escape_are_rejected(self):
        for jid in ('../outside', 'A'*32, 'short'):
            with self.assertRaisesRegex(ValueError, 'invalid_id'): recovery.source_path(self.root, jid)
        path = recovery.source_path(self.root, self.jid)
        path.unlink(); other = self.root/'outside'; other.write_bytes(b'complete source')
        try: path.symlink_to(other)
        except OSError: self.skipTest('symlink privilege unavailable')
        with self.assertRaisesRegex(ValueError, 'source_link'): self.resume()
        self.assertEqual(self.calls, [])

    @unittest.skipUnless(hasattr(os, 'mkfifo'), 'Unix FIFO check')
    def test_nonregular_reference_fails_without_waiting_for_a_writer(self):
        path = recovery.source_path(self.root, self.jid)
        path.unlink(); os.mkfifo(path)
        with self.assertRaisesRegex(ValueError, 'not_regular'): self.resume()
        self.assertEqual(self.calls, [])

    def test_journal_replace_failure_retains_previous_complete_state(self):
        recovery.save_jobs(self.db, self.jobs); before = self.db.read_bytes()
        with patch('rvc_recovery.os.replace', side_effect=OSError('synthetic full disk')):
            with self.assertRaises(OSError): recovery.save_jobs(self.db, {})
        self.assertEqual(self.db.read_bytes(), before)
        self.assertFalse(list(self.root.glob('music-jobs-*')))

    def test_real_http_restart_and_duplicate_broker_do_not_duplicate_active_compute(self):
        # Real child broker and HTTP handlers; replace ONLY model/backend work
        # inside this synthetic child. No production service or media is used.
        bootstrap = '''
import json,sys,time
from pathlib import Path
import urllib.error
import yue_bridge
state=Path(sys.argv[sys.argv.index('--state')+1])
def offline(*args,**kwargs):raise urllib.error.URLError('synthetic offline backend')
yue_bridge.urllib.request.urlopen=offline
yue_bridge.singing_profile=lambda config:json.loads(Path(config).read_text())
def convert(body,root,jid,config,profile,unused):
    import base64
    assert (state/(jid+'-source.audio')).read_bytes()==base64.b64decode(body['reference_base64'])
    with (state/'run-count').open('a') as f:f.write('1');f.flush()
    while not (state/'release').exists():time.sleep(.02)
    output=state/(jid+'-rvc.wav');output.write_bytes(b'synthetic completed output')
    return dict(file=output.name,bytes=output.stat().st_size,duration_seconds=90)
yue_bridge.rvc_cover.convert=convert
yue_bridge.main()
'''
        workflows = self.root/'workflows'; workflows.mkdir()
        for name in ('audio_yue2_music_cover.json','audio_yue2_text_to_music.json'):
            (workflows/name).write_text('{}')
        token = self.root/'token'; token.write_text('synthetic-token')
        config = self.root/'profile'; config.write_text(json.dumps(self.profile))
        self.job['state']='running'; recovery.save_jobs(self.db,self.jobs)
        env=dict(os.environ,PYTHONPATH=str(Path(__file__).parent.resolve()),PYTHONDONTWRITEBYTECODE='1')
        children=[]; client=urllib.request.build_opener(urllib.request.ProxyHandler({}))
        def start():
            with socket.socket() as sock:
                sock.bind(('127.0.0.1',0));port=sock.getsockname()[1]
            child=subprocess.Popen([sys.executable,'-c',bootstrap,'--project',str(self.root),
                '--workflow-dir',str(workflows),'--state',str(self.root),'--token-file',str(token),
                '--port',str(port),'--singing-voice-config',str(config)],env=env,
                stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,**no_console_options())
            children.append(child)
            return child,'http://127.0.0.1:'+str(port)
        def request(base,post=False,auth=True,body=None):
            data=json.dumps({} if body is None else body).encode() if post else None
            req=urllib.request.Request(base+'/jobs/'+self.jid+('/resume' if post else ''),data=data,
                headers={'Authorization':'Bearer '+('synthetic-token' if auth else 'wrong')})
            try:
                with client.open(req,timeout=2) as response:return response.status,json.load(response)
            except urllib.error.HTTPError as error:return error.code,json.load(error)
        def wait_for(check,child=None):
            deadline=time.monotonic()+10
            while time.monotonic()<deadline:
                if child is not None and child.poll() is not None:self.fail('synthetic broker exited')
                try:
                    if check():return
                except (OSError,ValueError):pass
                time.sleep(.03)
            self.fail('synthetic wait deadline')
        try:
            child,base=start()
            wait_for(lambda:request(base)[1].get('state')=='needs_review',child)
            self.assertEqual(request(base,True,auth=False)[0],401)
            self.assertEqual(request(base,True,body={'title':'override'})[0],400)
            self.assertEqual(request(base,True)[1]['id'],self.jid)
            wait_for(lambda:(self.root/'run-count').exists(),child)
            for _ in range(3):self.assertEqual(request(base,True)[0],200)
            self.assertEqual((self.root/'run-count').read_text(),'1')
            before=self.db.read_bytes();duplicate,_=start()
            self.assertNotEqual(duplicate.wait(timeout=10),0)
            self.assertEqual(self.db.read_bytes(),before)
            child.terminate();child.wait(timeout=10)
            child,base=start()
            wait_for(lambda:request(base)[1].get('state')=='needs_review',child)
            changed=dict(self.profile,config_sha256='changed');config.write_text(json.dumps(changed))
            self.assertEqual(request(base,True)[1]['error'],'rvc_recovery_profile_changed')
            config.write_text(json.dumps(self.profile))
            self.assertEqual(request(base,True)[0],200)
            wait_for(lambda:(self.root/'run-count').read_text()=='11',child)
            (self.root/'release').touch()
            wait_for(lambda:request(base)[1].get('state')=='completed',child)
            self.assertEqual(request(base,True)[1]['state'],'completed')
            self.assertEqual((self.root/'run-count').read_text(),'11')
            self.assertEqual(len(json.loads(self.db.read_text())),1)
            # A fresh submission has a recoverable source before its callback
            # starts, and the same submission key resolves the same job.
            def submit_new():
                req=urllib.request.Request(base+'/rvc-jobs',data=json.dumps(self.body).encode(),
                    headers={'Authorization':'Bearer synthetic-token','Idempotency-Key':'new-synthetic-request'})
                with client.open(req,timeout=2) as response:return json.load(response)
            new_id=submit_new()['id']
            self.assertNotEqual(new_id,self.jid)
            self.assertEqual(submit_new()['id'],new_id)
            self.assertEqual(recovery.source_path(self.root,new_id).read_bytes(),b'complete source')
            wait_for(lambda:(self.root/'run-count').read_text()=='111',child)
            self.assertEqual(len(json.loads(self.db.read_text())),2)
        finally:
            for child in children:
                if child.poll() is None:child.terminate()
            for child in children:
                try:child.wait(timeout=10)
                except subprocess.TimeoutExpired:child.kill();child.wait()


if __name__ == '__main__': unittest.main()
