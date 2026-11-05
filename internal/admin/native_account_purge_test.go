package admin

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestOfflineNativePurgeChecksLeaseIdentityAndKeepsOtherProfiles(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "admin")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, "accounts.json"), []byte(`[{"id":"second","removed":true,"purged":true},{"id":"other","removed":true}]`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "owner.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "registry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("PRAGMA user_version=2;CREATE TABLE access_tokens(accounts_json TEXT);INSERT INTO access_tokens VALUES('[\"second\"]'),('[\"second\",\"other\"]');CREATE TABLE accounts(id TEXT PRIMARY KEY,storage_mode TEXT,profile_path TEXT);CREATE TABLE account_settings(account_id TEXT REFERENCES accounts(id),alias TEXT);"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"second", "other"} {
		dir := filepath.Join(root, "accounts", id, "profile")
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec("INSERT INTO accounts VALUES(?,'managed',?)", id, dir); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec("INSERT INTO account_settings VALUES('second','primary-alias')"); err != nil {
		t.Fatal(err)
	}
	if FinishNativeAccountPurges(root, "primary-alias") == nil {
		t.Fatal("erased primary alias")
	}
	lease, err := lockNativeForCleanup(filepath.Join(root, "owner.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if FinishNativeAccountPurges(root, "primary") == nil {
		t.Fatal("cleaned while native owner was active")
	}
	lease.Close()
	for i := 0; i < 2; i++ {
		if err = FinishNativeAccountPurges(root, "primary"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = os.Stat(filepath.Join(root, "accounts", "second")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "accounts", "other", "profile")); err != nil {
		t.Fatal(err)
	}
	var tokenScopes string
	if err = db.QueryRow("SELECT group_concat(accounts_json) FROM access_tokens").Scan(&tokenScopes); err != nil || tokenScopes != `["other"]` {
		t.Fatal(tokenScopes, err)
	}
	var id string
	if err = db.QueryRow("SELECT id FROM accounts").Scan(&id); err != nil || id != "other" {
		t.Fatal(id, err)
	}
}
