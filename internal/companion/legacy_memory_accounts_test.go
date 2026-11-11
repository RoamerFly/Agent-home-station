package companion

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLegacyMemoryAccountDiscoveryVerifiedMetadata(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "accounts", true: "identity_mismatch"}[invalid], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "memory.sqlite")
			db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(memorySchema); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO meta(key,value) VALUES('schema','2')`); err != nil {
				t.Fatal(err)
			}
			for _, scope := range []Scope{{Channel: "wechat", Account: "old-b", Thread: "room"}, {Channel: "wechat", Account: "old-a", Thread: "room"}, {Channel: "wechat", Account: "old-a", Thread: "second-room"}, {Channel: "local", Account: "private-local", Thread: "local"}} {
				scope.ID = ScopeID(scope.Channel, scope.Account, scope.Thread)
				insertLegacyScope(t, db, scope.ID, scope)
			}
			if invalid {
				db.Exec(`UPDATE scopes SET payload=json_set(payload,'$.account','forged') WHERE id=?`, ScopeID("wechat", "old-a", "room"))
			}
			db.Close()
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			got, err := LegacyMemoryAccounts(t.Context(), file)
			if invalid {
				if err == nil {
					t.Fatal("forged identity accepted", got)
				}
				return
			}
			if err != nil || !slices.Equal(got, []string{"old-a", "old-b"}) {
				t.Fatal(got, err)
			}
		})
	}
}
