import copy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('mobile_workflow', Path(__file__).resolve().parents[1] / 'runtime/workflow.py')
workflow = importlib.util.module_from_spec(spec)
spec.loader.exec_module(workflow)


class WorkflowTest(unittest.TestCase):
    def setUp(self):
        self.snapshot = {'settings':{'revision':1,'enabled':True,'devices':['synthetic'],'actions':['tap','capture'],'max_steps':12,'max_seconds':300},'tasks':{},'workflows':{}}
        self.now = '2026-11-01T00:00:00Z'

    def apply(self, op, supplied=None, fact=None):
        result = workflow.transition({'op':op,'snapshot':self.snapshot,'input':supplied or {},'now':self.now,'nonce':'synthetic-source-bound-nonce','facts':fact or {}})
        if 'settings' in result:
            self.snapshot['settings'] = result['settings']
        self.snapshot['tasks'].update(result['tasks'])
        self.snapshot['workflows'].update(result['workflows'])
        return result

    def start(self, identifier='device-synthetic', request='same-request'):
        return self.apply('start', {'id':identifier,'request_hash':request,'source':{'account':'account-a','thread':'thread-a','sender':'owner','admin':True,'message_id':request},'device_id':'synthetic','goal':'Inspect the synthetic screen'})['result']

    def tick(self, value=None, error=None):
        metadata = self.snapshot['workflows']['device-synthetic']
        fact = {}
        if value is not None or error is not None:
            fact = {'effect_id':metadata['effect']['id']}
            fact.update({'error':error} if error else {'value':value})
        self.apply('advance', {'task_id':'device-synthetic'}, fact)
        return self.snapshot['workflows']['device-synthetic']

    def observation(self, identifier='obs-original', digest='a'):
        return {'id':identifier,'device_id':'synthetic','package':'synthetic.app','width':100,'height':200,'nodes':[],'png_sha256':digest*64}

    def test_idempotent_start_fifo_and_same_device_exclusion(self):
        first = self.start()
        again = self.start('device-duplicate')
        self.assertEqual(first, again)
        self.now = '2026-11-01T00:00:01Z'
        self.start('device-next', 'other-request')
        self.apply('advance')
        self.assertEqual(self.snapshot['tasks']['device-synthetic']['state'], 'running')
        self.assertEqual(self.snapshot['tasks']['device-next']['state'], 'queued')
        self.assertEqual(self.snapshot['workflows']['device-synthetic']['effect']['capability'], 'device.observe')

    def test_one_effect_per_transition_and_model_finish_fact(self):
        self.start()
        metadata = self.tick()
        self.assertEqual(metadata['effect']['capability'], 'device.observe')
        metadata = self.tick(self.observation())
        self.assertEqual(metadata['effect']['capability'], 'device.plan')
        metadata = self.tick({'step':{'action':'finish','summary':'Observed the synthetic screen','arguments':{}}})
        self.assertNotIn('effect', metadata)
        self.assertEqual(self.snapshot['tasks']['device-synthetic']['result'], 'Observed the synthetic screen')
        self.assertEqual(self.snapshot['tasks']['device-synthetic']['state'], 'completed')

    def test_changed_fresh_screen_never_submits_stale_coordinates(self):
        self.start(); self.tick(); self.tick(self.observation())
        self.tick({'step':{'action':'tap','arguments':{'x':5,'y':5}}})
        metadata = self.tick(self.observation('obs-fresh','b'))
        self.assertNotIn('effect', metadata)
        self.assertEqual(metadata['phase'], 'observe')
        self.assertEqual(self.snapshot['tasks']['device-synthetic']['steps'], 0)

    def test_action_intent_is_durable_before_dispatch_unknown_is_terminal(self):
        self.start(); self.tick(); self.tick(self.observation())
        self.tick({'step':{'action':'tap','arguments':{'x':5,'y':5}}})
        metadata = self.tick(self.observation('obs-fresh'))
        self.assertEqual(metadata['effect']['capability'], 'device.act')
        self.assertEqual(metadata['effect']['arguments']['request_key'], 'device-synthetic-step-00')
        self.assertEqual(self.snapshot['tasks']['device-synthetic']['steps'], 1)
        self.tick(error='device_action_outcome_unknown')
        self.assertEqual(self.snapshot['tasks']['device-synthetic']['state'], 'needs_review')
        self.assertNotIn('effect', self.snapshot['workflows']['device-synthetic'])

    def test_cancel_and_recovery_never_replay_pending_effect(self):
        self.start(); self.tick()
        recovered = copy.deepcopy(self.snapshot)
        self.apply('cancel', {'task_id':'device-synthetic'})
        self.assertEqual(self.snapshot['tasks']['device-synthetic']['state'], 'cancelled')
        self.assertEqual(self.snapshot['workflows']['device-synthetic'], {})
        self.snapshot = recovered
        self.apply('recover')
        self.assertEqual(self.snapshot['tasks']['device-synthetic']['state'], 'needs_review')
        self.assertEqual(self.snapshot['workflows']['device-synthetic'], {})

    def test_settings_revision_cancels_queued_and_running(self):
        self.start(); self.tick()
        config = copy.deepcopy(self.snapshot['settings'])
        config['enabled'] = False
        self.apply('save', {'settings':config})
        self.assertEqual(self.snapshot['settings']['revision'], 2)
        self.assertEqual(self.snapshot['tasks']['device-synthetic']['state'], 'cancelled')
        self.assertEqual(self.snapshot['workflows']['device-synthetic'], {})

    def test_capture_progress_uses_verified_offsets_without_binary_payloads(self):
        self.start(); self.tick(); self.tick(self.observation())
        self.tick({'step':{'action':'capture','arguments':{'seconds':4}}})
        self.tick(self.observation('obs-fresh','b'))
        artifact = {'id':'clip-synthetic','device_id':'synthetic','request_key':'device-synthetic-step-00','bytes':16384,'sha256':'c'*64,'kind':'screen_recording','mime':'video/mp4','audio':False,'original_media':False,'requested_seconds':4}
        metadata = self.tick({'state':'submitted','artifact':artifact})
        self.assertEqual(metadata['effect']['capability'], 'device.read_artifact')
        metadata = self.tick({'id':'clip-synthetic','offset':0,'next_offset':8192,'sha256':'c'*64,'complete':False})
        self.assertEqual(metadata['effect']['arguments']['offset'], 8192)
        metadata = self.tick({'id':'clip-synthetic','offset':8192,'next_offset':16384,'sha256':'c'*64,'complete':True})
        self.assertEqual(metadata['effect']['capability'], 'artifact.analyze')
        self.tick({'summary':'Actual synthetic recording','asset_id':'synthetic-asset'})
        self.assertEqual(self.snapshot['tasks']['device-synthetic']['artifacts'][0]['asset_id'], 'synthetic-asset')


if __name__ == '__main__':
    unittest.main()
