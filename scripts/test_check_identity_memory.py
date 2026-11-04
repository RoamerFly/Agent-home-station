"""Contract tests for the privacy-preserving identity-memory rehearsal snapshot."""

import json
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
CHECK = ROOT / "scripts" / "check_identity_memory.py"


SCHEMA = """
CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE scopes(id TEXT PRIMARY KEY,payload BLOB NOT NULL);
CREATE TABLE events(id TEXT PRIMARY KEY,scope TEXT,seq INTEGER,trace TEXT,role TEXT,sender TEXT,at TEXT,text TEXT,hash TEXT,complete INTEGER,revision INTEGER,payload BLOB,suppressed INTEGER,active INTEGER);
CREATE TABLE observations(event_id TEXT,hash TEXT,text TEXT);
CREATE TABLE event_revisions(event_id TEXT,revision INTEGER,text TEXT,hash TEXT);
CREATE TABLE event_terms(term TEXT,event_id TEXT,scope TEXT);
CREATE TABLE jobs(scope TEXT PRIMARY KEY,processed_seq INTEGER,state TEXT,error TEXT,updated TEXT);
CREATE TABLE memories(id TEXT PRIMARY KEY,scope TEXT,subject TEXT,kind TEXT,status TEXT,version INTEGER,pinned INTEGER,updated TEXT,payload BLOB,statement_at TEXT,event_at TEXT,event_year INTEGER);
CREATE TABLE memory_sources(memory_id TEXT,event_id TEXT,quote TEXT);
CREATE TABLE memory_terms(term TEXT,memory_id TEXT,scope TEXT);
CREATE TABLE memory_revisions(memory_id TEXT,version INTEGER,payload BLOB);
CREATE TABLE embeddings(memory_id TEXT,space TEXT,version INTEGER,vector BLOB);
CREATE TABLE maintenance_usage(day TEXT,purpose TEXT,reported INTEGER,tokens INTEGER,requests INTEGER);
CREATE TABLE memory_blocks(scope TEXT,event_id TEXT);
CREATE TABLE rebuild_requests(scope TEXT PRIMARY KEY,requested TEXT);
CREATE TABLE search_documents(namespace TEXT,id TEXT,scope TEXT,kind TEXT,at TEXT,text TEXT,payload BLOB,hash TEXT);
CREATE TABLE source_conflicts(scope TEXT,event_id TEXT,other_event_id TEXT,reason TEXT);
CREATE TABLE context_metrics(id INTEGER PRIMARY KEY,at TEXT,total INTEGER,bytes INTEGER,components TEXT,method TEXT);
"""


class CheckIdentityMemoryTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.db_path = self.root / "memory.sqlite"
        db = sqlite3.connect(self.db_path)
        db.executescript(SCHEMA)
        db.execute("INSERT INTO meta VALUES ('schema','2')")
        for account, scope, event, memory in (("first", "scope-a", "event-a", "memory-a"), ("second", "scope-b", "event-b", "memory-b")):
            db.execute("INSERT INTO scopes VALUES (?,?)", (scope, json.dumps({"account": account}).encode()))
            db.execute("INSERT INTO events VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)", (event, scope, 1, event, "user", account, "t", "private-body-" + account, "hash", 1, 1, b"{}", 0, 1))
            db.execute("INSERT INTO memories VALUES (?,?,?,?,?,?,?,?,?,?,?,?)", (memory, scope, account, "fact", "active", 1, 0, "t", b"{}", "", "", 0))
            db.execute("INSERT INTO memory_sources VALUES (?,?,?)", (memory, event, "private-source-" + account))
        db.execute("INSERT INTO maintenance_usage VALUES ('today','maintenance',1,1,1)")
        db.commit()
        db.close()

    def tearDown(self):
        self.temp.cleanup()

    def run_check(self, *args):
        return subprocess.run([sys.executable, str(CHECK), "--database", str(self.db_path), *args], text=True, capture_output=True, check=False)

    def test_complete_snapshot_hides_private_values_and_can_be_compared(self):
        snapshot = self.root / "before.json"
        result = self.run_check("--output", str(snapshot))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("private-body-first", result.stdout)
        self.assertNotIn("private-source-first", result.stdout)
        data = json.loads(result.stdout)
        self.assertEqual(data["table_count"], len(SCHEMA.split("CREATE TABLE")) - 1)
        self.assertEqual(data["tables"]["events"]["rows"], 2)
        self.assertEqual(data["tables"]["memories"]["rows"], 2)
        self.assertEqual(data["tables"]["maintenance_usage"]["rows"], 1)
        again = self.run_check("--compare", str(snapshot))
        self.assertEqual(again.returncode, 0, again.stderr)
        self.assertTrue(json.loads(again.stdout)["matches_comparison"])

    def test_any_persisted_change_changes_complete_snapshot_and_output_never_overwrites(self):
        before = self.root / "before.json"
        self.assertEqual(self.run_check("--output", str(before)).returncode, 0)
        db = sqlite3.connect(self.db_path)
        db.execute("UPDATE events SET text='changed-other-account' WHERE id='event-b'")
        db.commit()
        db.close()
        result = self.run_check("--compare", str(before))
        self.assertEqual(result.returncode, 1, result.stderr)
        data = json.loads(result.stdout)
        self.assertFalse(data["matches_comparison"])
        overwrite = self.run_check("--output", str(before))
        self.assertEqual(overwrite.returncode, 2)


if __name__ == "__main__":
    unittest.main()
