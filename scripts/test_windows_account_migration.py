import hashlib
from contextlib import closing
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest

from migrate_windows_account import prepare, scope_id


class WindowsAccountMigrationTests(unittest.TestCase):
    def source(self, base):
        source = base / 'source'
        (source / 'companion').mkdir(parents=True)
        old = scope_id('wechat', 'old', 'synthetic-room')
        scope = {'id': old, 'channel': 'wechat', 'account': 'old', 'thread': 'synthetic-room',
                 'notes': ['old'], 'messages': [{'text': '/linux/private and old'}]}
        text = '用户原文 /linux/private，否定和更正必须保留'
        digest = hashlib.sha256(text.encode()).hexdigest()
        with closing(sqlite3.connect(source / 'companion/memory.sqlite')) as db:
            db.executescript('CREATE TABLE scopes(id TEXT PRIMARY KEY,payload BLOB);'
                             'CREATE TABLE events(id TEXT PRIMARY KEY,scope TEXT,text TEXT,hash TEXT,payload BLOB);'
                             'CREATE TABLE memories(id TEXT PRIMARY KEY,scope TEXT,status TEXT,payload BLOB);'
                             'CREATE TABLE memory_blocks(scope TEXT,event_id TEXT,PRIMARY KEY(scope,event_id));')
            db.execute('INSERT INTO scopes VALUES(?,?)', (old, json.dumps(scope).encode()))
            db.execute('INSERT INTO events VALUES(?,?,?,?,?)', ('original', old, text, digest, json.dumps({'text': text}).encode()))
            db.execute('INSERT INTO memories VALUES(?,?,?,?)', ('fact', old, 'forgotten', json.dumps({'scope': old, 'detail': 'old'}).encode()))
            db.execute('INSERT INTO memory_blocks VALUES(?,?)', (old, 'original'))
            db.commit()
        (source / 'settings.json').write_text(json.dumps({'account': 'old', 'path': '/linux/private/data', 'text': 'old'}))
        return source, old, text, digest

    def test_plan_is_read_only_and_apply_preserves_original_provenance_and_forgetting(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            source, old, text, digest = self.source(base)
            before = {p.relative_to(source): p.read_bytes() for p in source.rglob('*') if p.is_file()}
            target = base / 'new'
            paths = {'/linux/private': str(base / 'windows')}
            report = prepare(source, target, 'old', 'new', paths)
            self.assertFalse(report['applied'])
            self.assertFalse(target.exists())
            report = prepare(source, target, 'old', 'new', paths, True)
            new = scope_id('wechat', 'new', 'synthetic-room')
            self.assertEqual(report['scope_mapping'], {old: new})
            with closing(sqlite3.connect(target / 'companion/memory.sqlite')) as db:
                self.assertEqual(db.execute('SELECT id,text,hash,scope FROM events').fetchone(), ('original', text, digest, new))
                self.assertEqual(db.execute('SELECT status,scope FROM memories').fetchone(), ('forgotten', new))
                self.assertEqual(db.execute('SELECT scope,event_id FROM memory_blocks').fetchone(), (new, 'original'))
                scope = json.loads(db.execute('SELECT payload FROM scopes').fetchone()[0])
                self.assertEqual(scope['account'], 'new')
                self.assertEqual(scope['notes'], ['old'])
                self.assertEqual(scope['messages'][0]['text'], '/linux/private and old')
            settings = json.loads((target / 'settings.json').read_text())
            self.assertEqual(settings, {'account': 'new', 'path': str(base / 'windows/data'), 'text': 'old'})
            self.assertEqual(before, {p.relative_to(source): p.read_bytes() for p in source.rglob('*') if p.is_file()})
            with self.assertRaisesRegex(ValueError, 'target_must_be_new'):
                prepare(source, target, 'old', 'new', paths, True)

    def test_uncheckpointed_source_is_rejected_before_creating_target(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            source, _, _, _ = self.source(base)
            (source / 'companion/memory.sqlite-wal').write_bytes(b'uncommitted synthetic journal')
            with self.assertRaisesRegex(ValueError, 'requires_offline_checkpoint'):
                prepare(source, base / 'new', 'old', 'new', {}, True)
            self.assertFalse((base / 'new').exists())

    def test_wrong_source_account_rejected_before_creating_target(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            source, _, _, _ = self.source(base)
            with self.assertRaisesRegex(ValueError, 'unexpected_source_account'):
                prepare(source, base / 'new', 'other', 'new', {}, True)
            self.assertFalse((base / 'new').exists())


if __name__ == '__main__':
    unittest.main()
