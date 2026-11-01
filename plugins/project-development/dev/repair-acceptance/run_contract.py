"""Run the preserved synthetic repair acceptance contract in the repo test harness.

No arguments print help. The production application never registers these
routes. --test uses only synthetic test fixtures; no provider, WeChat or device
calls are made. Linux sandbox checks require Bubblewrap and otherwise skip.
"""
import argparse
import json
from pathlib import Path
import subprocess
import tempfile


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    action = parser.add_mutually_exclusive_group()
    action.add_argument('--test', action='store_true', help='Run the preserved synthetic repair tests')
    action.add_argument('--vet', action='store_true', help='Vet the preserved sources against the admin test harness')
    args = parser.parse_args(argv)
    if not args.test and not args.vet:
        parser.print_help()
        return 0
    directory = Path(__file__).resolve().parent
    repository = directory.parents[3]
    if not (repository / 'go.mod').is_file():
        parser.error('this developer contract requires the original repository admin test harness')
    replacements = {
        str(repository / 'internal/admin' / source.name.removesuffix('.in')): str(source)
        for source in directory.glob('*.go.in')
    }
    # The former registration is injected only into this temporary test build.
    # No source, application configuration, task or user file is modified.
    server = repository / 'internal/admin/server.go'
    source = server.read_text(encoding='utf-8')
    anchor = '\ts.agentRoutes(mux)\n'
    if source.count(anchor) != 1 or 's.repairTestRoutes(mux)' in source:
        parser.error('production route layout changed; review this developer overlay')
    with tempfile.TemporaryDirectory(prefix='wechat-repair-contract-') as temporary:
        generated = Path(temporary).resolve()
        if generated.parent != Path(tempfile.gettempdir()).resolve():
            raise RuntimeError('unexpected temporary overlay directory')
        overlaid_server = generated / 'server.go'
        overlaid_server.write_text(source.replace(anchor, anchor + '\ts.repairTestRoutes(mux)\n'), encoding='utf-8')
        replacements[str(server)] = str(overlaid_server)
        overlay = generated / 'overlay.json'
        overlay.write_text(json.dumps({'Replace': replacements}), encoding='utf-8')
        command = ['go', 'vet' if args.vet else 'test', '-overlay', str(overlay), './internal/admin']
        if args.test:
            command += ['-run', '^TestRepair', '-count=1', '-v']
        return subprocess.run(command, cwd=repository, check=False).returncode


if __name__ == '__main__':
    raise SystemExit(main())
