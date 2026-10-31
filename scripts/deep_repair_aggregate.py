"""Aggregate independently isolated groups without mixing protocols or source."""
import argparse
import hashlib
import json
import re
from pathlib import Path


def fatal_provider_row(row):
    # Compatibility with the preserved pre-failfast reports. Only the fixed,
    # sanitized diagnostics field is inspected; user/model text is not a signal.
    text = row.get('error') or ''
    return bool(re.fullmatch(
        r'(?:model_insufficient_balance stage=[a-z_]+ http=402|'
        r'model_authentication_failed stage=[a-z_]+ http=40[13]) attempts=\d+', text))


def incomplete_status(reports, report_paths, manual_path):
    rows = [row for report in reports for row in report['results']]
    blocked = any(report.get('provider_blocked') for report in reports) or any(fatal_provider_row(row) for row in rows)
    unassessed = [row['id'] for row in rows if set(row.get('rubric', {})) != {
        'correct_person', 'supported_facts', 'correct_time', 'useful_response'} or fatal_provider_row(row)]
    return {
        'version': 'deep-repair-incomplete-aggregation-v1',
        'production_sha256': reports[0]['production_sha256'],
        'model_config': reports[0].get('model_config'),
        'requested_cases': sum(report.get('requested_cases', len(report['results'])) for report in reports),
        'cases': len(rows), 'attempted_cases': len(rows),
        'assessed_cases': len(rows) - len(unassessed),
        'unassessed_attempted_case_ids': unassessed,
        'remaining_cases': sum(report.get('remaining_cases', max(0, report.get('requested_cases', len(report['results'])) - len(report['results']))) for report in reports),
        'pending_case_ids': [case_id for report in reports for case_id in report.get('pending_case_ids', [])],
        'blocked_case_ids': sorted({row['id'] for row in rows if fatal_provider_row(row)} | {
            report['provider_block']['case_id'] for report in reports
            if (report.get('provider_block') or {}).get('case_id')}),
        'continuations': [report.get('continuation') for report in reports if report.get('continuation')],
        'provider_blocked': blocked, 'partial': True,
        'assessment_complete': False, 'model_assessment_complete': False,
        'quality_gate_passed': None, 'model_quality_gate_passed': None,
        'full240_accuracy': None, 'release_gate_passed': False,
        'report_files': [str(path) for path in report_paths],
        'manual_audit': str(manual_path), 'manual_audit_used_for_quality': False,
        'method': 'Incomplete provider/checkpoint state; no missing cases are graded as semantic failures and no accuracy is inferred from attempts.',
        'automatic_resume': False, 'regenerate_completed_generation': False,
        'synthetic_only': True, 'wechat_sends': 0,
    }


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--reports', nargs='+', type=Path, required=True)
    p.add_argument('--manual-audit', type=Path, required=True)
    p.add_argument('--output', type=Path, required=True)
    args = p.parse_args()
    reports = [json.loads(path.read_text(encoding='utf8')) for path in args.reports]
    identity = ('version', 'production_sha256', 'protocol_sha256',
                'fixture_sha256', 'model', 'os', 'arch', 'go')
    for report in reports:
        if report.get('wechat_sends') != 0 or not report.get('synthetic_only'):
            raise SystemExit('only synthetic, zero-send reports may be combined')
        if any(report.get(key) != reports[0].get(key) for key in identity):
            raise SystemExit('source, protocol, fixture, model or environment differs')
        # The runner's config_sha256 intentionally also covers case selection,
        # so isolated groups have different run hashes. Compare the actual safe
        # model configuration and retain every selection hash independently.
        if report.get('model_config') != reports[0].get('model_config'):
            raise SystemExit('model endpoint or generation parameters differ')
    rows = [row for report in reports for row in report['results']]
    ids = [row['id'] for row in rows]
    root = Path(__file__).resolve().parents[1]
    catalogue = json.loads((root / 'internal/companion/testdata/deep_repair/scenarios.json').read_text(encoding='utf8'))
    catalogue_ids = {row['id'] for row in catalogue['cases']}
    if len(ids) != len(set(ids)) or not set(ids).issubset(catalogue_ids):
        raise SystemExit('reports contain duplicate or unknown catalogue cases')
    if any(report.get('partial') or report.get('provider_blocked') for report in reports) or any(fatal_provider_row(row) for row in rows):
        result = incomplete_status(reports, args.reports, args.manual_audit)
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding='utf8')
        print(json.dumps({key: result[key] for key in (
            'version', 'production_sha256', 'requested_cases', 'attempted_cases',
            'assessed_cases', 'remaining_cases', 'provider_blocked', 'partial',
            'assessment_complete', 'model_quality_gate_passed', 'release_gate_passed')}, ensure_ascii=False))
        raise SystemExit(1)
    if len(ids) != 240 or len(set(ids)) != 240 or set(ids) != {row['id'] for row in catalogue['cases']}:
        raise SystemExit('the reports must cover each of the fixed 240 cases exactly once')
    audit = json.loads(args.manual_audit.read_text(encoding='utf8'))
    if audit.get('production_sha256') != reports[0]['production_sha256']:
        raise SystemExit('manual audit belongs to a different production source')
    reviewed = set(audit.get('reviewed_case_ids', []))
    critical = set(audit.get('key_wrong_person_case_ids', [])) | set(audit.get('key_wrong_media_attribution_case_ids', []))
    manual_errors = set(audit.get('confirmed_error_case_ids', []))
    if not reviewed.issubset(ids) or not manual_errors.issubset(reviewed) or not critical.issubset(manual_errors):
        raise SystemExit('manual findings must identify reviewed catalogue cases')
    required_review = {row['id'] for row in catalogue['cases'] if row['group'] in ('people', 'media', 'conversation')}
    if not required_review.issubset(reviewed):
        raise SystemExit('all 120 people/media/conversation cases need source review')
    automatic_errors = {row['id'] for row in rows if not row.get('structural_pass') or set(row.get('rubric', {})) != {'correct_person', 'supported_facts', 'correct_time', 'useful_response'} or not all(row['rubric'].values())}
    errors = automatic_errors | manual_errors
    required = [row for row in rows if row['must_respond']]
    responded = sum(row.get('action') == 'reply' and bool(row.get('messages')) for row in required)
    passed = len(rows) - len(errors)
    gate = passed * 100 >= 95 * len(rows) and responded * 100 >= 95 * len(required) and not critical
    usage = [entry for report in reports for entry in report.get('usage', [])]
    result = {key: reports[0].get(key) for key in identity}
    result['model_config'] = reports[0]['model_config']
    result['model_configuration_sha256'] = hashlib.sha256(json.dumps(result['model_config'], sort_keys=True).encode()).hexdigest()
    result['run_selection_hashes'] = {str(path): report['config_sha256'] for path, report in zip(args.reports, reports)}
    result.update(method='six isolated groups; identical fixed production/protocol/fixture/model; no pooled duplicate cases',
                  cases=len(rows), automatic_passed=len(rows)-len(automatic_errors), manually_corrected_passed=passed,
                  required_response_cases=len(required), responded=responded, reviewed_cases=len(reviewed),
                  critical_error_case_ids=sorted(critical), error_case_ids=sorted(errors),
                  provider_reported_input=sum(entry.get('input', 0) for entry in usage if entry.get('reported')),
                  provider_reported_output=sum(entry.get('output', 0) for entry in usage if entry.get('reported')),
                  provider_reported_cache_hit=sum(entry.get('cache_hit', 0) for entry in usage if entry.get('reported')),
                  usage_calls=len(usage), report_files=[str(path) for path in args.reports],
                  manual_audit=str(args.manual_audit), wechat_sends=0, quality_gate_passed=gate)
    result.update(assessment_complete=True, model_assessment_complete=True,
                  model_quality_gate_passed=gate)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding='utf8')
    print(json.dumps(result, ensure_ascii=False))
    raise SystemExit(0 if gate else 1)


if __name__ == '__main__':
    main()
