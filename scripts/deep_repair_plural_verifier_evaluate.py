"""Run two frozen synthetic plural-pronoun verifier cases; never connect a native sender."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
from deep_repair_evaluate import production_manifest, report_status, refuse_provider_blocked_resume


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--config', required=True, type=Path)
    parser.add_argument('--report', required=True, type=Path)
    parser.add_argument('--resume', action='store_true')
    parser.add_argument('--timeout-seconds', type=int, default=3600)
    args = parser.parse_args()
    if args.timeout_seconds <= 0:
        raise SystemExit('timeout must be positive')
    root = Path(__file__).resolve().parents[1]
    config = json.loads(args.config.read_text(encoding='utf8'))
    if not all(config.get(k) for k in ('base_url', 'model', 'key_file')):
        raise SystemExit('config requires base_url, model, key_file; do not copy credentials')
    report = args.report.resolve()
    report.parent.mkdir(parents=True, exist_ok=True)
    if args.resume:
        refuse_provider_blocked_resume(report)
    manifest = production_manifest(root)
    manifest['driver_sha256'] = hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
    manifest_path = report.with_suffix('.source-manifest.json')
    if args.resume and manifest_path.exists():
        old = json.loads(manifest_path.read_text(encoding='utf8'))
        if (old['sha256'], old['driver_sha256']) != (manifest['sha256'], manifest['driver_sha256']):
            raise SystemExit('source/driver changed; choose a new report')
    manifest_path.write_text(json.dumps(manifest, indent=2), encoding='utf8')
    env = dict(os.environ,
               WECHAT_DEEP_REPAIR_MODEL_CONFIG=str(args.config.resolve()),
               WECHAT_DEEP_REPAIR_PLURAL_VERIFIER_REPORT=str(report),
               WECHAT_DEEP_REPAIR_SOURCE_MANIFEST=str(manifest_path))
    if args.resume:
        env['WECHAT_DEEP_REPAIR_PLURAL_VERIFIER_RESUME'] = '1'
    result = subprocess.run(['go', 'test', './internal/companion', '-run',
                             '^TestDeepRepairPluralVerifierRealModel$', '-count=1', '-v',
                             '-timeout', f'{args.timeout_seconds}s'], cwd=root, env=env)
    if report.exists():
        data = json.loads(report.read_text(encoding='utf8'))
        print(json.dumps(report_status(data, (
            'version', 'cases', 'bad_rejected', 'bad_cases', 'good_accepted',
            'good_cases', 'passed', 'partial', 'production_sha256')), ensure_ascii=False))
    raise SystemExit(result.returncode)


if __name__ == '__main__':
    main()
