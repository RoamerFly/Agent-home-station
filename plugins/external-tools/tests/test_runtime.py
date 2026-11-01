import base64
import importlib.util
import io
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('external_tools', Path(__file__).parents[1] / 'runtime' / 'main.py')
runtime = importlib.util.module_from_spec(spec); spec.loader.exec_module(runtime)

PROTOCOL = {'version': 1, 'tools': [{'name':'voice__speak','description':'Synthetic fixed tool.','input_schema':{'type':'object'},'text_path':'result.text','media_kind':'voice','fingerprint':'a'*64}]}
CONTEXT = {'account':'a','thread':'t','requester':'r','purpose':'p','request_key':'k'}
ARG_HASH = 'c'*64
TICKET_HASH = 'd'*64

class RuntimeTests(unittest.TestCase):
    def test_json_output_uses_only_exact_tool_intent(self):
        calls=[]
        result=runtime.execute({'phase':'call','tool':'voice__speak','arguments':{'text':'x'},'arguments_sha256':ARG_HASH,'protocol':PROTOCOL}, lambda x: calls.append(x) or {'content_type':'application/json','body_base64':base64.b64encode(b'{"result":{"text":"ok"}}').decode()})
        self.assertEqual(calls,[{'phase':'call','tool':'voice__speak','arguments_sha256':ARG_HASH}])
        self.assertEqual(result['output'],{'text':'ok','media':[]})
        self.assertNotIn('endpoint',json.dumps(calls))

    def test_binary_output_requires_declared_media_kind(self):
        binary=base64.b64encode(b'synthetic').decode()
        result=runtime.execute({'phase':'call','tool':'voice__speak','arguments':{},'arguments_sha256':ARG_HASH,'protocol':PROTOCOL}, lambda _: {'content_type':'audio/wav','body_base64':binary})
        self.assertEqual(result['output']['media'][0]['data_base64'],binary)
        bad={'version':1,'tools':[dict(PROTOCOL['tools'][0], media_kind='')]}
        with self.assertRaises(ValueError): runtime.execute({'phase':'call','tool':'voice__speak','arguments':{},'arguments_sha256':ARG_HASH,'protocol':bad}, lambda _: {'content_type':'audio/wav','body_base64':binary})

    def test_large_binary_uses_private_host_reference(self):
        ref = {'content_type':'audio/wav','binary_ref':{'sha256':'b'*64,'size':25*1024*1024}}
        result = runtime.execute({'phase':'call','tool':'voice__speak','arguments':{},'arguments_sha256':ARG_HASH,'protocol':PROTOCOL}, lambda _: ref)
        self.assertEqual(result['output']['media'], [{'kind':'voice','host_binary_ref':'b'*64}])

    def test_legacy_names_and_string_at_compatibility(self):
        name = 'a'*64 + '__' + 'b'*64
        protocol = {'version':1,'tools':[dict(PROTOCOL['tools'][0], name=name, text_path='value')]}
        for payload, expected in ((b'{"value":null}', ''), (b'{"value":true}', 'true'), (b'{"value":false}', 'false'), (b'{"value":9007199254740993}', '9007199254740993')):
            if not expected:
                with self.assertRaises(ValueError): runtime.execute({'phase':'call','tool':name,'arguments':{},'arguments_sha256':ARG_HASH,'protocol':protocol}, lambda _: {'content_type':'application/json','body_base64':base64.b64encode(payload).decode()})
            else:
                result = runtime.execute({'phase':'call','tool':name,'arguments':{},'arguments_sha256':ARG_HASH,'protocol':protocol}, lambda _: {'content_type':'application/json','body_base64':base64.b64encode(payload).decode()})
                self.assertEqual(result['output']['text'], expected)

    def test_empty_legacy_description_is_accepted(self):
        protocol = {'version':1,'tools':[dict(PROTOCOL['tools'][0], description='')]}
        self.assertIn('voice__speak', runtime.protocol(protocol))

    def test_string_at_map_and_slice_match_go_fmt_style(self):
        protocol = {'version':1,'tools':[dict(PROTOCOL['tools'][0], text_path='value')]}
        payload = b'{"value":{"z":[true,null,9007199254740993],"a":"x"}}'
        result = runtime.execute({'phase':'call','tool':'voice__speak','arguments':{},'arguments_sha256':ARG_HASH,'protocol':protocol}, lambda _: {'content_type':'text/plain','body_base64':base64.b64encode(payload).decode()})
        self.assertEqual(result['output']['text'], 'map[a:x z:[true <nil> 9007199254740993]]')

    def test_job_steps_never_loop_and_keep_ticket_identity(self):
        job_protocol={'version':1,'tool':dict(PROTOCOL['tools'][0], job={'id_path':'task.id'}, poll={'status_path':'state','done_values':['done'],'failed_values':['failed'],'text_path':'result.text'})}
        ticket={'tool':'voice__speak','fingerprint':'a'*64,'state':'submitting'}
        calls=[]
        started=runtime.job_execute({'phase':'start','tool':'voice__speak','arguments':{'n':9007199254740993},'arguments_sha256':ARG_HASH,'ticket':ticket,'ticket_sha256':TICKET_HASH,'protocol':job_protocol}, lambda x:calls.append(x) or {'content_type':'application/json','body_base64':base64.b64encode(b'{"task":{"id":"remote-1"}}').decode()})
        self.assertEqual(calls,[{'phase':'start','tool':'voice__speak','arguments_sha256':ARG_HASH,'ticket_sha256':TICKET_HASH}])
        waited=runtime.job_execute({'phase':'poll','tool':'voice__speak','arguments':{},'arguments_sha256':ARG_HASH,'ticket':started['ticket'],'ticket_sha256':TICKET_HASH,'protocol':job_protocol}, lambda _: {'content_type':'application/json','body_base64':base64.b64encode(b'{"state":"queued"}').decode()})
        self.assertEqual(waited['ticket']['state'],'waiting')
        done=runtime.job_execute({'phase':'poll','tool':'voice__speak','arguments':{},'arguments_sha256':ARG_HASH,'ticket':started['ticket'],'ticket_sha256':TICKET_HASH,'protocol':job_protocol}, lambda _: {'content_type':'application/json','body_base64':base64.b64encode(b'{"state":"done","result":{"text":"ok"}}').decode()})
        self.assertEqual(done['ticket']['state'],'completed')
        self.assertEqual(done['ticket']['remote_id'],'remote-1')

    def test_job_start_can_fail_without_remote_id(self):
        protocol={'version':1,'tool':dict(PROTOCOL['tools'][0], job={'id_path':'task.id'}, poll={'status_path':'state','done_values':['done'],'failed_values':['failed'],'text_path':'result.text'})}
        ticket={'tool':'voice__speak','fingerprint':'a'*64,'state':'submitting'}
        result=runtime.job_execute({'phase':'start','tool':'voice__speak','arguments':{},'arguments_sha256':ARG_HASH,'ticket':ticket,'ticket_sha256':TICKET_HASH,'protocol':protocol}, lambda _:{'content_type':'application/json','body_base64':base64.b64encode(b'{"state":"failed"}').decode()})
        self.assertEqual(result['ticket']['state'],'failed')
        self.assertNotIn('remote_id',result['ticket'])

    def test_job_start_can_complete_without_remote_id(self):
        protocol={'version':1,'tool':dict(PROTOCOL['tools'][0], job={'id_path':'task.id'}, poll={'status_path':'state','done_values':['done'],'failed_values':['failed'],'text_path':'result.text'})}
        ticket={'tool':'voice__speak','fingerprint':'a'*64,'state':'submitting'}
        result=runtime.job_execute({'phase':'start','tool':'voice__speak','arguments':{},'arguments_sha256':ARG_HASH,'ticket':ticket,'ticket_sha256':TICKET_HASH,'protocol':protocol}, lambda _:{'content_type':'application/json','body_base64':base64.b64encode(b'{"state":"done","result":{"text":"now"}}').decode()})
        self.assertEqual(result['ticket']['state'],'completed')
        self.assertEqual(result['ticket']['output']['text'],'now')
        self.assertNotIn('remote_id',result['ticket'])

    def test_job_start_keeps_known_id_when_immediate_output_is_invalid(self):
        protocol={'version':1,'tool':dict(PROTOCOL['tools'][0], job={'id_path':'task.id'}, poll={'status_path':'state','done_values':['done'],'failed_values':['failed'],'text_path':'result.text'})}
        ticket={'tool':'voice__speak','fingerprint':'a'*64,'state':'submitting'}
        result=runtime.job_execute({'phase':'start','tool':'voice__speak','arguments':{},'arguments_sha256':ARG_HASH,'ticket':ticket,'ticket_sha256':TICKET_HASH,'protocol':protocol}, lambda _:{'content_type':'application/json','body_base64':base64.b64encode(b'{"task":{"id":"remote-2"},"state":"done","result":{}}').decode()})
        self.assertTrue(result['known_remote_only'])
        self.assertEqual(result['ticket']['state'],'waiting')
        self.assertEqual(result['ticket']['remote_id'],'remote-2')
        self.assertEqual(result['ticket']['output'],{'text':'','media':[]})

    def test_job_start_keeps_numeric_id_on_missing_status(self):
        protocol={'version':1,'tool':dict(PROTOCOL['tools'][0], job={'id_path':'task.id'}, poll={'status_path':'state','done_values':['done'],'failed_values':['failed'],'text_path':'result.text'})}
        ticket={'tool':'voice__speak','fingerprint':'a'*64,'state':'submitting'}
        result=runtime.job_execute({'phase':'start','tool':'voice__speak','arguments':{},'arguments_sha256':ARG_HASH,'ticket':ticket,'ticket_sha256':TICKET_HASH,'protocol':protocol}, lambda _:{'content_type':'application/json','body_base64':base64.b64encode(b'{"task":{"id":18446744073709551615}}').decode()})
        self.assertTrue(result['known_remote_only'])
        self.assertEqual(result['ticket']['remote_id'],'18446744073709551615')

    def test_job_cancel_allows_completed_ticket_with_remote_id(self):
        protocol={'version':1,'tool':dict(PROTOCOL['tools'][0], job={'id_path':'task.id'}, poll={'status_path':'state','done_values':['done'],'failed_values':['failed'],'text_path':'result.text'})}
        ticket={'tool':'voice__speak','fingerprint':'a'*64,'state':'completed','remote_id':'remote-1','output':{'text':'saved','media':[]}}
        result=runtime.job_execute({'phase':'cancel','tool':'voice__speak','arguments':{},'arguments_sha256':ARG_HASH,'ticket':ticket,'ticket_sha256':TICKET_HASH,'protocol':protocol}, lambda _:{'content_type':'application/json','body_base64':base64.b64encode(b'{}').decode()})
        self.assertTrue(result['cancel_ack'])
        self.assertEqual(result['ticket'],ticket)

    def test_json_media_url_is_projected_for_host_validation(self):
        media_protocol={'version':1,'tools':[dict(PROTOCOL['tools'][0], text_path='', media_url_path='result.audio')]}
        reply={'content_type':'application/json','body_base64':base64.b64encode(b'{"result":{"audio":"https://example.test/a.wav"}}').decode()}
        result=runtime.execute({'phase':'call','tool':'voice__speak','arguments':{},'arguments_sha256':ARG_HASH,'protocol':media_protocol}, lambda _:reply)
        self.assertEqual(result['output']['media'],[{'kind':'voice','url':'https://example.test/a.wav'}])

    def test_legacy_json_detection_accepts_plain_scalars_but_not_bad_declared_json(self):
        scalar = {'content_type':'text/plain','body_base64':base64.b64encode(b'true').decode()}
        with self.assertRaises(ValueError):
            runtime.execute({'phase':'call','tool':'voice__speak','arguments':{},'arguments_sha256':ARG_HASH,'protocol':PROTOCOL}, lambda _:scalar)
        invalid = {'content_type':'application/json','body_base64':base64.b64encode(b'not-json').decode()}
        with self.assertRaises(ValueError):
            runtime.execute({'phase':'call','tool':'voice__speak','arguments':{},'arguments_sha256':ARG_HASH,'protocol':PROTOCOL}, lambda _:invalid)

    def test_rejects_async_or_dynamic_protocol(self):
        for phase in ('start','poll','cancel'):
            with self.assertRaises(ValueError): runtime.execute({'phase':phase,'tool':'voice__speak','arguments':{},'arguments_sha256':ARG_HASH,'protocol':PROTOCOL}, lambda _: self.fail('host called'))
        bad={'version':1,'tools':[dict(PROTOCOL['tools'][0], endpoint='https://example.test')]}
        with self.assertRaises(ValueError): runtime.protocol(bad)

    def test_stdio_contract(self):
        call={'phase':'call','tool':'voice__speak','arguments':{'text':9007199254740993},'arguments_sha256':ARG_HASH,'protocol':PROTOCOL}
        incoming=io.StringIO('\n'.join(json.dumps(x) for x in [
            {'jsonrpc':'2.0','id':1,'method':'initialize','params':{}},
            {'jsonrpc':'2.0','id':2,'method':'tools/list','params':{}},
            {'jsonrpc':'2.0','id':3,'method':'tools/call','params':{'name':'external.http_request','arguments':call,'_meta':{'wechat/context':CONTEXT}}},
            {'jsonrpc':'2.0','id':'external-request-1','result':{'content_type':'application/json','body_base64':base64.b64encode(b'{"result":{"text":"ok"}}').decode()}},
        ])+'\n')
        outgoing=io.StringIO(); runtime.Wire(incoming,outgoing).serve(); replies=[json.loads(x) for x in outgoing.getvalue().splitlines()]
        self.assertEqual(replies[1]['result']['tools'][0]['name'],'external.http_request')
        self.assertEqual(replies[1]['result']['tools'][1]['name'],'external.job_request')
        self.assertEqual(replies[2]['method'],'wechat/external_request')
        self.assertEqual(replies[2]['params'],{'phase':'call','tool':'voice__speak','arguments_sha256':ARG_HASH})
        self.assertEqual(replies[3]['result']['output']['text'],'ok')

if __name__ == '__main__': unittest.main()
