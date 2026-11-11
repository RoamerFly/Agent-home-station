package companion

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestRetainedLegacyMemoryIncludesColdOriginalsWithoutRestoringForgotten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.sqlite")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(memorySchema); err != nil {
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO meta(key,value) VALUES('schema','2')`)
	account, thread := "synthetic-old", "synthetic-room"
	scope := Scope{ID: ScopeID("wechat", account, thread), Channel: "wechat", Account: account, Thread: thread}
	insertLegacyScope(t, db, scope.ID, scope)
	for i, id := range []string{"cold", "blocked", "suppressed", "revised", "hot"} {
		revision := 1
		if id == "revised" {
			revision = 2
		}
		insertBoundLegacyEvent(t, db, id, scope.ID, account, thread, i+1, revision, "synthetic "+id)
	}
	db.Exec(`UPDATE events SET active=0 WHERE id!='hot'`)
	db.Exec(`UPDATE events SET suppressed=1 WHERE id='suppressed'`)
	db.Exec(`INSERT INTO memory_blocks(scope,event_id) VALUES(?,?)`, scope.ID, "blocked")
	insertBoundLegacyFact(t, db, "cold-fact", scope.ID, "cold", "synthetic cold", 1, false)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	before := sha256.Sum256(raw)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	recent, err := ReadLegacyMemorySelection(context.Background(), f, account, thread)
	if err != nil || len(recent.Events) != 1 || recent.Events[0].EventID != "hot" {
		t.Fatal(recent, err)
	}
	retained, err := ReadRetainedLegacyMemorySelection(context.Background(), f, account, thread)
	if err != nil || len(retained.Events) != 2 || len(retained.Memories) != 1 {
		t.Fatal(retained, err)
	}
	if retained.Events[0].EventID != "cold" || retained.Events[1].EventID != "hot" {
		t.Fatal(retained.Events)
	}
	raw, _ = os.ReadFile(path)
	if sha256.Sum256(raw) != before {
		t.Fatal("immutable source mutated")
	}
}
