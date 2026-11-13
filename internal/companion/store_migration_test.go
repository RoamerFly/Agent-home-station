package companion

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// A crash in the middle of the v1 backfill must not leave a table that refuses
// to open with "duplicate column name", and the rewrite must not run as one
// unbounded transaction.
func TestUpgradeMemoryTimesIsIdempotent(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		`CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT NOT NULL)`,
		`CREATE TABLE events(id TEXT PRIMARY KEY,scope TEXT NOT NULL,seq INTEGER NOT NULL,trace TEXT NOT NULL,role TEXT NOT NULL,sender TEXT NOT NULL,at TEXT NOT NULL,text TEXT NOT NULL,hash TEXT NOT NULL,complete INTEGER NOT NULL,revision INTEGER NOT NULL DEFAULT 1,payload BLOB NOT NULL)`,
		`CREATE TABLE memories(id TEXT PRIMARY KEY,scope TEXT NOT NULL,subject TEXT NOT NULL,kind TEXT NOT NULL,status TEXT NOT NULL,version INTEGER NOT NULL,pinned INTEGER NOT NULL DEFAULT 0,updated TEXT NOT NULL,payload BLOB NOT NULL)`,
		`INSERT INTO meta(key,value) VALUES('schema','1')`,
		`INSERT INTO events(id,scope,seq,trace,role,sender,at,text,hash,complete,payload) VALUES('e1','s1',1,'t1','user','owner','2025-01-02T03:04:05.000000001Z','hello','h1',1,x'7b7d')`,
		`INSERT INTO memories(id,scope,subject,kind,status,version,updated,payload) VALUES('m1','s1','owner','preference','active',1,'2025-01-02T03:04:05Z',x'7b7d')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := upgradeMemoryTimes(db); err != nil {
			t.Fatalf("migration attempt %d: %v", attempt, err)
		}
	}
	var version, eventAt, statementAt string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='schema'`).Scan(&version); err != nil || version != "2" {
		t.Fatalf("schema marker = %q err=%v", version, err)
	}
	if err := db.QueryRow(`SELECT at FROM events WHERE id='e1'`).Scan(&eventAt); err != nil || eventAt == "" {
		t.Fatalf("event time was not normalised: %q err=%v", eventAt, err)
	}
	if err := db.QueryRow(`SELECT statement_at FROM memories WHERE id='m1'`).Scan(&statementAt); err != nil || statementAt == "" {
		t.Fatalf("memory statement time was not backfilled: %q err=%v", statementAt, err)
	}
}
