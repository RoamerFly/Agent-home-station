"""Sampling graph and native contract tests, with synthetic node metadata."""
import json
from pathlib import Path
import unittest
from yue_bridge import prepare_song_graph, song_recipe
import yue_sampling as sampling

WORKFLOWS=Path(__file__).resolve().parents[1]/'components/yue2/app/workflows_api'


def native_info():
    info={}
    for _,kind,keys in sampling.GROUPS.values():
        fields={}
        for key in keys:
            if key in ('sampler_name','scheduler'):
                fields[key]=[['dpm_2','euler'] if key=='sampler_name' else ['sgm_uniform','normal'],{}]
            else:
                integer=key in ('top_k','steps','max_abc_tokens','penalty_window')
                fields[key]=['INT' if integer else 'FLOAT',{'min':1 if integer else 0,'max':32768 if integer else 10}]
        info[kind]={'input':{'required':fields}}
    return info


class SamplingTests(unittest.TestCase):
    def test_nondefault_including_zero_reaches_nodes_and_recipe(self):
        body=dict(mode='original',title='合成',style='folk',lyrics='[Verse]\n合成词',seed=123,duration_seconds=10,
                  planning_mode='melody',sampling={'semantic':{'temperature':0,'top_k':8},'render':{'steps':57,'cfg':0,'sampler_name':'euler','scheduler':'normal','denoise':.8},'score':{'temperature':.3,'max_abc_tokens':7000}})
        body.update(sampling.resolve(body,WORKFLOWS,native_info()))
        graph=prepare_song_graph(WORKFLOWS,body,'synthetic',Path('unused'))
        for group,(node,_,_) in sampling.GROUPS.items():
            for key,value in body['sampling'][group].items():
                self.assertEqual(graph[node]['inputs'][key],value)
        self.assertEqual(graph['33:24']['inputs']['mode'],'melody')
        self.assertEqual(graph['33:25']['inputs']['mode'],'melody')
        self.assertEqual(song_recipe(body)['sampling'],body['sampling'])
        self.assertEqual(song_recipe(body)['planning_mode'],'melody')

    def test_defaults_are_workflow_values_not_provider_defaults(self):
        info=native_info();info['KSampler']['input']['required']['steps'][1]['default']=20
        resolved=sampling.resolve({},WORKFLOWS,info)
        self.assertEqual(resolved['sampling']['render']['steps'],32)
        self.assertEqual(resolved['planning_mode'],'full')
        self.assertIn('score',resolved['sampling'])
        self.assertTrue(sampling.options(WORKFLOWS,'cover',info)['native_limits_verified'])

    def test_invalid_parameters_reject_instead_of_clamping_or_ignoring(self):
        info=native_info()
        for overrides in (
            {'sampling':{'semantic':{'temperature':-1}}},
            {'sampling':{'render':{'steps':1.2}}},
            {'sampling':{'render':{'steps':True}}},
            {'sampling':{'render':{'steps':10**1000}}},
            {'sampling':{'render':{'sampler_name':'invented'}}},
            {'sampling':{'semantic':{'unknown':1}}},
            {'sampling':{'unknown':{}}},
            {'sampling':{'semantic':None}},
            {'sampling':[]},
            {'planning_mode':'off','sampling':{'score':{'temperature':.5}}},
            {'mode':'cover','sampling':{'score':{'temperature':.5}}},
            {'mode':'cover','planning_mode':'full'},
        ):
            with self.subTest(overrides=overrides), self.assertRaises(ValueError):
                sampling.resolve(overrides,WORKFLOWS,info)
        info['KSampler']['input']['required']['steps'][1]['max']=80
        self.assertEqual(sampling.resolve({'sampling':{'render':{'steps':79}}},WORKFLOWS,info)['sampling']['render']['steps'],79)
        with self.assertRaisesRegex(ValueError,'outside_native_spec'):
            sampling.resolve({'sampling':{'render':{'steps':81}}},WORKFLOWS,info)

    def test_off_mode_excludes_score_and_switches_real_graph(self):
        body=dict(mode='original',planning_mode='off',style='folk',lyrics='synthetic',seed=1,duration_seconds=10)
        body.update(sampling.resolve(body,WORKFLOWS,native_info()))
        graph=prepare_song_graph(WORKFLOWS,body,'synthetic',Path('unused'))
        self.assertNotIn('score',body['sampling'])
        self.assertFalse(graph['33:31']['inputs']['value'])


if __name__=='__main__':unittest.main()
