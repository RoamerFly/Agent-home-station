"""Run five isolated same-candidate production review E2E rounds; never connect a native sender."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
from deep_repair_evaluate import production_manifest, report_status


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--config', required=True, type=Path)
    parser.add_argument('--report', required=True, type=Path)
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
    manifest = production_manifest(root)
    manifest['driver_sha256'] = hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
    manifest_path = report.with_suffix('.source-manifest.json')
    manifest_path.write_text(json.dumps(manifest, indent=2), encoding='utf8')
    env = dict(os.environ,
               WECHAT_DEEP_REPAIR_MODEL_CONFIG=str(args.config.resolve()),
               WECHAT_DEEP_REPAIR_REVIEW_E2E_REPORT=str(report),
               WECHAT_DEEP_REPAIR_SOURCE_MANIFEST=str(manifest_path))
    result = subprocess.run(['go', 'test', './internal/companion', '-run',
                             '^TestDeepRepairGeneratedReviewE2ERealModel$', '-count=1', '-v',
                             '-timeout', f'{args.timeout_seconds}s'], cwd=root, env=env)
    if report.exists():
        data = json.loads(report.read_text(encoding='utf8'))
        print(json.dumps(report_status(data, (
            'version', 'cases', 'passed_cases', 'passed', 'partial', 'production_sha256')), ensure_ascii=False))
    raise SystemExit(result.returncode)


if __name__ == '__main__':
    main()
