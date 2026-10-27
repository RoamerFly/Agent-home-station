"""Verified rollback executable retention. No credentials, media or databases.

The default command only prints a plan. Apply requires that exact plan hash.
Unknown names, links, corrupt files and active/previous-version hashes remain.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import stat

BACKUP = re.compile(r'^(wechat-admin|wechat-bot|wechat-sendd|wx)\.previous-([0-9a-f]{12})$')


def running_hashes(directory):
    """Protect mapped old Linux executables even after their path was replaced."""
    result = set()
    proc = Path('/proc')
    if not proc.is_dir():
        return result
    for entry in proc.iterdir():
        if not entry.name.isdecimal():
            continue
        try:
            target = os.readlink(entry / 'exe').removesuffix(' (deleted)')
            mapped = Path(target)
            if mapped.parent == directory and (mapped.name in ('wechat-admin', 'wechat-bot', 'wechat-sendd', 'wx') or BACKUP.fullmatch(mapped.name)):
                result.add(digest(entry / 'exe'))
        except OSError:
            continue
    return result


def digest(path):
    h = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            h.update(chunk)
    return h.hexdigest()


def fingerprint(path):
    before = path.lstat()
    if not stat.S_ISREG(before.st_mode) or path.is_symlink():
        raise ValueError('regular executable required')
    with path.open('rb') as stream:
        if stream.read(4) != b'\x7fELF':
            raise ValueError('ELF executable required')
    sha = digest(path)
    after = path.lstat()
    if (before.st_ino, before.st_dev, before.st_size, before.st_mtime_ns) != (after.st_ino, after.st_dev, after.st_size, after.st_mtime_ns):
        raise ValueError('executable changed during inspection')
    return {'name': path.name, 'bytes': before.st_size, 'mtime_ns': before.st_mtime_ns,
            'inode': before.st_ino, 'device': before.st_dev, 'sha256': sha}


def plan_retention(directory, keep=3, protect_names=()):
    original = Path(directory).absolute()
    directory = original.resolve(strict=True)
    if original != directory or not directory.is_dir() or isinstance(keep, bool) or keep < 1:
        raise ValueError('use a canonical directory and retain at least one rollback per program')
    groups, skipped, current = {}, [], {}
    mapped_hashes = running_hashes(directory)
    for path in directory.iterdir():
        match = BACKUP.fullmatch(path.name)
        if not match:
            continue
        program, suffix = match.groups()
        try:
            if program not in current:
                current[program] = fingerprint(directory / program)
            record = fingerprint(path)
            if not record['sha256'].startswith(suffix):
                raise ValueError('backup name/hash mismatch')
        except (OSError, ValueError):
            skipped.append(path.name)
            continue
        groups.setdefault(program, []).append(record)
    remove, retained = [], []
    for program, records in sorted(groups.items()):
        records.sort(key=lambda x: (x['mtime_ns'], x['name']), reverse=True)
        for index, record in enumerate(records):
            if index < keep or record['sha256'] == current[program]['sha256'] or record['sha256'] in mapped_hashes or record['name'] in protect_names:
                retained.append(record)
            else:
                remove.append(record)
    plan = {'root': str(directory), 'keep_per_program': keep, 'protect_names': sorted(protect_names),
            'active': current, 'remove': remove, 'retained': retained, 'skipped': sorted(skipped),
            'running_sha256': sorted(mapped_hashes),
            'reclaim_bytes': sum(x['bytes'] for x in remove)}
    plan['plan_hash'] = hashlib.sha256(json.dumps(plan, sort_keys=True).encode()).hexdigest()
    return plan


def apply_retention(plan, expected_hash):
    if plan['plan_hash'] != expected_hash:
        raise ValueError('plan hash does not match')
    root = Path(plan['root'])
    fresh = plan_retention(root, plan['keep_per_program'], plan['protect_names'])
    if fresh != plan:
        raise ValueError('executable inventory changed; inspect a fresh plan')
    # Validate every target before removing the first file. No recursive delete.
    for entry in plan['remove']:
        path = root / entry['name']
        if not BACKUP.fullmatch(entry['name']) or path.resolve().parent != root or fingerprint(path) != entry:
            raise ValueError('target changed or escaped the selected directory')
    removed, reclaimed = [], 0
    for entry in plan['remove']:
        path = root / entry['name']
        if fingerprint(path) != entry:
            raise ValueError('target changed before deletion')
        path.unlink()
        removed.append(entry['name'])
        reclaimed += entry['bytes']
    return {'removed': removed, 'reclaimed_bytes': reclaimed, 'retained_count': len(plan['retained'])}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--directory', required=True)
    parser.add_argument('--keep', type=int, default=3)
    parser.add_argument('--apply-plan-hash')
    parser.add_argument('--plan-file', help='Save private local plan; never commit it')
    args = parser.parse_args()
    plan = plan_retention(args.directory, args.keep)
    if args.plan_file:
        path = Path(args.plan_file)
        with path.open('x', encoding='utf8') as stream:
            json.dump(plan, stream, ensure_ascii=False, indent=2)
        os.chmod(path, 0o600)
    result = apply_retention(plan, args.apply_plan_hash) if args.apply_plan_hash else plan
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
