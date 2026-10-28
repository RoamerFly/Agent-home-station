"""Launch project background workers without allocating a Windows console.

Scheduled tasks enter through pythonw, so Windows Terminal never receives a
console application to host. Private request files keep Unicode argv intact.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys


def spawn(arguments, cwd, stdout, stderr):
    flags = getattr(subprocess, 'CREATE_NO_WINDOW', 0) | getattr(subprocess, 'CREATE_NEW_PROCESS_GROUP', 0)
    with open(stdout, 'ab', buffering=0) as out, open(stderr, 'ab', buffering=0) as err:
        return subprocess.Popen(arguments, cwd=cwd, stdin=subprocess.DEVNULL, stdout=out,
                                stderr=err, close_fds=True, creationflags=flags)


def request_launch(path):
    path = Path(path)
    result = {}
    try:
        data = json.loads(path.read_text(encoding='utf-8-sig'))
        arguments = data['arguments']
        if not isinstance(arguments, list) or not arguments or any(not isinstance(x, str) or '\0' in x for x in arguments):
            raise ValueError('invalid arguments')
        child = spawn(arguments, data['cwd'], data['stdout'], data['stderr'])
        result = {'pid': child.pid, 'console_allocated': False}
    except Exception as error:
        result = {'error': type(error).__name__ + ': ' + str(error)}
    pending = path.with_suffix('.pending')
    pending.write_text(json.dumps(result), encoding='utf8')
    pending.replace(path.with_suffix('.result.json'))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--request')
    parser.add_argument('--watch-yue', action='store_true')
    parser.add_argument('--keep-wsl', action='store_true')
    parser.add_argument('--distro')
    parser.add_argument('--linux-user')
    parser.add_argument('--marker')
    parser.add_argument('--workspace')
    parser.add_argument('--project')
    parser.add_argument('--shell')
    args = parser.parse_args()
    if args.request:
        return request_launch(args.request)
    if args.keep_wsl:
        root = Path(args.workspace).resolve()
        logs = root / 'temp/launcher'
        logs.mkdir(parents=True, exist_ok=True)
        command = ['wsl.exe', '-d', args.distro, '-u', args.linux_user, '--', '/bin/bash', '-c', 'exec -a '+args.marker+' sleep infinity']
        # Called only by our manual task with a generated hex workspace marker.
        if not args.marker.startswith('wechat-agent-anchor-') or not all(c in '0123456789abcdefABCDEF' for c in args.marker[len('wechat-agent-anchor-'):]):
            parser.error('invalid workspace marker')
        return spawn(command, root, logs/'wsl-anchor.stdout.log', logs/'wsl-anchor.stderr.log').wait()
    if args.watch_yue:
        root = Path(args.workspace).resolve()
        logs = root / 'temp'
        logs.mkdir(exist_ok=True)
        arguments = [args.shell, '-NoLogo', '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass',
                     '-File', str(root / 'scripts/Watch-Local-YuE.ps1'), '-Project', args.project]
        return spawn(arguments, root, logs/'yue-watchdog.stdout.log', logs/'yue-watchdog.stderr.log').wait()
    parser.error('select --request or --watch-yue')


if __name__ == '__main__':
    sys.exit(main())
