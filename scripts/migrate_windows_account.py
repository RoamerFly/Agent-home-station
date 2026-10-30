"""Prepare an offline Windows management copy; never modify the source/login.

The caller must stop the source and destination managers and verify that the
two account IDs belong to the same WeChat login. This only stages management
data in a NEW directory. It does not activate services or migrate native keys,
inbox cursors, Linux runtimes, or model environments. Private path-map JSON is
{old_absolute_prefix: new_absolute_prefix}. The default operation is a plan.
"""
import argparse
from contextlib import closing
import hashlib
import json
import os
from pathlib import Path
import sqlite3

from migrate_managed_workspaces import copy_verified
from migration_io import is_link


TEXT_FIELDS = {'text', 'content', 'summary', 'detail', 'notes', 'quote', 'prompt',
               'system_prompt', 'lyrics', 'transcript', 'instruction', 'error', 'reason'}


def scope_id(channel, account, thread):
    return hashlib.sha256('\0'.join((channel, account, thread)).encode()).hexdigest()


def transform(value, identities, paths, field=''):
    if field.lower() in TEXT_FIELDS:
        return value
    if isinstance(value, dict):
        return {identities.get(k, k): transform(v, identities, paths, k) for k, v in value.items()}
    if isinstance(value, list):
        return [transform(v, identities, paths, field) for v in value]
    if isinstance(value, str):
        if value in identities:
            return identities[value]
        for old, new in sorted(paths.items(), key=lambda x: len(x[0]), reverse=True):
            if value == old or value.startswith(old + '/') or value.startswith(old + '\\'):
                suffix = value[len(old):].replace('\\', '/').strip('/')
                return str(Path(new).joinpath(*suffix.split('/'))) if suffix else new
    return value


def memory_mapping(database, old, new):
    identities = {old: new}
    if not database.is_file():
        raise ValueError('source_memory_missing')
    with closing(sqlite3.connect(database.resolve().as_uri() + '?mode=ro', uri=True)) as db:
        for sid, payload in db.execute('SELECT id,payload FROM scopes'):
            scope = json.loads(payload)
            if scope['account'] != old or scope_id(scope['channel'], old, scope['thread']) != sid:
                raise ValueError('unexpected_source_account_or_scope')
            identities[sid] = scope_id(scope['channel'], new, scope['thread'])
    return identities


def rekey_memory(database, identities, paths):
    """Keep event IDs, original text/hash, provenance and all revision links."""
    with closing(sqlite3.connect(database)) as db:
        if db.execute('PRAGMA quick_check').fetchone()[0] != 'ok':
            raise ValueError('source_memory_integrity_failed')
        protected = list(db.execute('SELECT id,text,hash FROM events ORDER BY id'))
        counts = {}
        tables = [r[0] for r in db.execute("SELECT name FROM sqlite_master WHERE type='table'")]
        with db:
            for table in tables:
                if not table.replace('_', '').isalnum():
                    raise ValueError('unexpected_memory_table')
                columns = [r[1] for r in db.execute(f'PRAGMA table_info("{table}")')]
                counts[table] = db.execute(f'SELECT count(*) FROM "{table}"').fetchone()[0]
                # Only identity and structured payload columns may be rewritten.
                selected = [c for c in columns if c in {'scope', 'payload'} or table == 'scopes' and c == 'id']
                if not selected:
                    continue
                rows = list(db.execute(f'SELECT rowid,{",".join(selected)} FROM "{table}"'))
                for row in rows:
                    values, changed = [], False
                    for column, original in zip(selected, row[1:]):
                        value = original
                        if column == 'payload' and original is not None:
                            parsed = json.loads(original)
                            updated = transform(parsed, identities, paths)
                            if updated != parsed:
                                value = json.dumps(updated, ensure_ascii=False, separators=(',', ':')).encode()
                        elif isinstance(original, str):
                            value = identities.get(original, original)
                        values.append(value)
                        changed |= value != original
                    if changed:
                        assignment = ','.join('"' + c + '"=?' for c in selected)
                        db.execute(f'UPDATE "{table}" SET {assignment} WHERE rowid=?', (*values, row[0]))
            if protected != list(db.execute('SELECT id,text,hash FROM events ORDER BY id')):
                raise ValueError('original_event_bytes_changed')
            for table, count in counts.items():
                if db.execute(f'SELECT count(*) FROM "{table}"').fetchone()[0] != count:
                    raise ValueError('memory_row_count_changed')
        db.execute('PRAGMA wal_checkpoint(TRUNCATE)')
        if db.execute('PRAGMA quick_check').fetchone()[0] != 'ok':
            raise ValueError('target_memory_integrity_failed')
    return counts


def prepare(source, target, old, new, paths, apply=False):
    source, target = Path(source).absolute(), Path(target).absolute()
    if target.exists() or source == target or source in target.parents:
        raise ValueError('target_must_be_new_and_outside_source')
    for p in (source, target.parent):
        for ancestor in (p, *p.parents):
            try:
                metadata = ancestor.lstat()
            except FileNotFoundError:
                continue
            if is_link(metadata):
                raise ValueError('linked_migration_path')
    files = []
    for parent, dirs, names in os.walk(source, followlinks=False):
        for name in dirs + names:
            if is_link((Path(parent) / name).lstat()):
                raise ValueError('linked_source_entry')
        for name in names:
            files.append(Path(parent) / name)
    database = source / 'companion/memory.sqlite'
    # Offline copies must not detach an uncheckpointed database from its journal.
    for suffix in ('-wal', '-journal'):
        journal = database.with_name(database.name + suffix)
        if journal.exists() and journal.stat().st_size:
            raise ValueError('source_memory_requires_offline_checkpoint')
    identities = memory_mapping(database, old, new)
    report = {'source_files': len(files), 'source_bytes': sum(p.stat().st_size for p in files),
              'scopes': len(identities) - 1, 'applied': apply,
              'login_migrated': False, 'native_cursor_migrated': False}
    if not apply:
        return report
    target.mkdir(parents=True, exist_ok=False)
    for p in files:
        copy_verified(p, target / p.relative_to(source))
    report['memory_rows'] = rekey_memory(target / 'companion/memory.sqlite', identities, paths)
    changed = 0
    for p in target.rglob('*.json'):
        before = p.read_bytes()
        try:
            value = json.loads(before.decode('utf-8-sig'))
        except (ValueError, UnicodeError):
            continue
        updated = transform(value, identities, paths)
        if updated != value:
            p.write_text(json.dumps(updated, ensure_ascii=False, indent=2) + '\n', encoding='utf8')
            changed += 1
    report['structured_files_rebased'] = changed
    report['scope_mapping'] = {k: v for k, v in identities.items() if k != old}
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('source-admin', 'target-admin', 'from-account', 'to-account', 'path-map'):
        parser.add_argument('--' + name, required=True)
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    paths = json.loads(Path(args.path_map).read_text(encoding='utf-8-sig'))
    report = prepare(args.source_admin, args.target_admin, args.from_account, args.to_account, paths, args.apply)
    print(json.dumps(report, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
