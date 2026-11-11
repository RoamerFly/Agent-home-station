package companion

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetainedLegacyMemoryCarriesOfflineProfileProof(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.sqlite")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, query := range []string{memorySchema, `INSERT INTO meta(key,value) VALUES('schema','2')`} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	account, thread := "synthetic-account", "synthetic-thread"
	scope := Scope{ID: ScopeID("wechat", account, thread), Channel: "wechat", Account: account, Thread: thread}
	insertLegacyScope(t, db, scope.ID, scope)
	generation := strings.Repeat("a", 64)
	for index, fixture := range []struct{ id, generation, integrity string }{
		{"proved", generation, "verified_import"},
		{"bad-generation", "invalid", "verified_import"},
		{"unverified", generation, "observed"},
	} {
		insertBoundLegacyEvent(t, db, fixture.id, scope.ID, account, thread, index+1, 1, "合成原话")
		if _, err := db.Exec(`UPDATE events SET payload=json_set(CAST(payload AS TEXT),'$.source.integrity',?,'$.source.offline_profile_generation',?) WHERE id=?`, fixture.integrity, fixture.generation, fixture.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	selection, err := ReadRetainedLegacyMemorySelection(t.Context(), f, account, thread)
	if err != nil || len(selection.Events) != 1 || selection.Events[0].EventID != "proved" || selection.Events[0].OfflineProfileGeneration != generation {
		t.Fatalf("unexpected offline proof projection: %+v, %v", selection, err)
	}
}
