"""Offline report/control regressions; no model, service or native sender."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from deep_repair_evaluate import report_status, refuse_provider_blocked_resume


ROOT = Path(__file__).resolve().parents[1]
CATALOGUE = json.loads((ROOT / 'internal/companion/testdata/deep_repair/scenarios.json').read_text(encoding='utf8'))['cases']


class FailfastReports(unittest.TestCase):
    def report(self, ids, **updates):
        data = {'version': 'offline-fixture', 'production_sha256': 'a' * 64,
                'protocol_sha256': 'b' * 64, 'fixture_sha256': 'c' * 64,
                'config_sha256': 'd' * 64, 'model': 'synthetic', 'os': 'offline',
                'arch': 'offline', 'go': 'offline', 'model_config': {'model': 'synthetic'},
                'partial': False, 'synthetic_only': True, 'wechat_sends': 0,
                'results': [{'id': case_id, 'must_respond': True, 'action': 'reply',
                             'messages': ['合成验收回复。'], 'structural_pass': True,
                             'rubric': dict.fromkeys(('correct_person', 'supported_facts', 'correct_time', 'useful_response'), True)} for case_id in ids]}
        data.update(cases=len(ids), requested_cases=len(ids))
        data.update(updates)
        return data

    def aggregate(self, report, audit=None):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            source, manual, output = path / 'source.json', path / 'audit.json', path / 'aggregate.json'
            source.write_text(json.dumps(report), encoding='utf8')
            if audit is None:
                audit = {'production_sha256': 'a' * 64,
                         'reviewed_case_ids': [row['id'] for row in CATALOGUE if row['group'] in ('people', 'media', 'conversation')],
                         'confirmed_error_case_ids': [], 'key_wrong_person_case_ids': [], 'key_wrong_media_attribution_case_ids': []}
            manual.write_text(json.dumps(audit), encoding='utf8')
            process = subprocess.run([sys.executable, str(ROOT / 'scripts/deep_repair_aggregate.py'),
                                      '--reports', str(source), '--manual-audit', str(manual), '--output', str(output)],
                                     capture_output=True, text=True, encoding='utf8', timeout=15)
            self.assertTrue(output.exists(), process.stderr)
            return process.returncode, json.loads(output.read_text(encoding='utf8'))

    def test_blocked_partial_keeps_quality_unknown_and_release_false(self):
        data = self.report([CATALOGUE[0]['id']], requested_cases=240, partial=True,
                           attempted_cases=1, remaining_cases=239, provider_blocked=True,
                           provider_block={'case_id': CATALOGUE[0]['id']},
                           pending_case_ids=[CATALOGUE[1]['id']])
        data['results'][0].update(error='model_insufficient_balance stage=http_status http=402 attempts=1', rubric={}, structural_pass=False)
        code, out = self.aggregate(data)
        self.assertEqual(code, 1)
        self.assertIsNone(out['model_quality_gate_passed'])
        self.assertIsNone(out['full240_accuracy'])
        self.assertFalse(out['assessment_complete'])
        self.assertFalse(out['release_gate_passed'])
        self.assertEqual((out['attempted_cases'], out['remaining_cases'], out['assessed_cases']), (1, 239, 0))

    def test_nonblocked_checkpoint_is_unknown_too(self):
        code, out = self.aggregate(self.report([CATALOGUE[0]['id']], partial=True, requested_cases=240))
        self.assertEqual(code, 1)
        self.assertIsNone(out['quality_gate_passed'])
        self.assertFalse(out['provider_blocked'])

    def test_preserved_legacy_http402_is_not_scored_as_239_semantic_errors(self):
        data = self.report([row['id'] for row in CATALOGUE])
        for row in data['results'][1:]:
            row.update(error='model_insufficient_balance stage=http_status http=402 attempts=1', rubric={}, structural_pass=False)
        code, out = self.aggregate(data)
        self.assertEqual(code, 1)
        self.assertIsNone(out['full240_accuracy'])
        self.assertEqual(out['assessed_cases'], 1)
        self.assertNotIn('manually_corrected_passed', out)

    def test_complete_original_95_percent_and_key_gates_unchanged(self):
        data = self.report([row['id'] for row in CATALOGUE])
        for row in data['results'][:12]:
            row['rubric']['supported_facts'] = False
        code, out = self.aggregate(data)
        self.assertEqual((code, out['manually_corrected_passed']), (0, 228))
        self.assertTrue(out['model_quality_gate_passed'])
        data['results'][12]['rubric']['supported_facts'] = False
        code, out = self.aggregate(data)
        self.assertEqual((code, out['manually_corrected_passed']), (1, 227))
        self.assertFalse(out['model_quality_gate_passed'])
        # A single reviewed key attribution error still blocks quality even
        # when all automatic answers satisfy the 95% threshold.
        data = self.report([row['id'] for row in CATALOGUE])
        first = CATALOGUE[0]['id']
        audit = {'production_sha256': 'a' * 64,
                 'reviewed_case_ids': [row['id'] for row in CATALOGUE if row['group'] in ('people', 'media', 'conversation')],
                 'confirmed_error_case_ids': [first], 'key_wrong_person_case_ids': [first],
                 'key_wrong_media_attribution_case_ids': []}
        code, out = self.aggregate(data, audit)
        self.assertEqual(code, 1)
        self.assertFalse(out['model_quality_gate_passed'])

    def test_status_and_resume_do_not_drop_blocked_phase(self):
        data = {'cases': 1, 'provider_blocked': True, 'partial': True,
                'remaining_cases': 2, 'continuation': {'blocked_phase': 'rubric', 'regenerate_completed_generation': False}}
        self.assertEqual(report_status(data, ('cases',))['continuation'], data['continuation'])
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'report.json'
            path.write_text(json.dumps(data), encoding='utf8')
            original = path.read_bytes()
            with self.assertRaises(SystemExit):
                refuse_provider_blocked_resume(path)
            self.assertEqual(path.read_bytes(), original)


if __name__ == '__main__':
    unittest.main()
