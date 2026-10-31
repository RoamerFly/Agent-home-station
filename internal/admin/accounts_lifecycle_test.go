package admin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAccountRemoveRestoreAndRename(t *testing.T) {
	f, a := setupAccounts(t)
	path := "/api/accounts/second/manage"
	if code, _, _ := f.request("POST", path, map[string]string{"action": "remove"}, true, false); code != 403 {
		t.Fatalf("CSRF: %d", code)
	}
	if code, _, _ := f.request("POST", "/api/accounts/test/manage", map[string]string{"action": "remove"}, true, true); code != 409 {
		t.Fatalf("primary: %d", code)
	}
	second := a.lookup("second")
	configPath := filepath.Join(second.manager.dir, "config.json")
	before, _ := os.ReadFile(configPath)
	for _, action := range []string{"rename", "remove"} {
		if code, b, _ := f.request("POST", path, map[string]string{"action": action, "label": "备用测试"}, true, true); code != 200 {
			t.Fatalf("%s: %d %s", action, code, b)
		}
	}
	if code, _, _ := f.request("GET", "/api/status?account=second", nil, true, false); code != 404 {
		t.Fatalf("removed route: %d", code)
	}
	// Refresh must not reimport the account returned by the native directory.
	f.request("GET", "/api/accounts", nil, true, false)
	if !a.lookup("second").record.Removed {
		t.Fatal("refresh resurrected account")
	}
	restarted, err := newAccountRegistry(a.root)
	if err != nil {
		t.Fatal(err)
	}
	if restored := restarted.lookup("second"); restored == nil || !restored.record.Removed || restored.manager != nil {
		t.Fatal("restart resurrected removed account")
	}
	restarted.close()
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Fatal("configuration changed")
	}
	data, _ := os.ReadFile(filepath.Join(f.manager.dir, "accounts.json"))
	var records []accountRecord
	if json.Unmarshal(data, &records) != nil || len(records) != 1 || !records[0].Removed || records[0].Label != "备用测试" {
		t.Fatal("tombstone not persisted")
	}
	if code, b, _ := f.request("POST", path, map[string]string{"action": "restore"}, true, true); code != 200 {
		t.Fatalf("restore %d %s", code, b)
	}
	if code, _, _ := f.request("GET", "/api/status?account=second", nil, true, false); code != 200 {
		t.Fatalf("restored route: %d", code)
	}
	if a.lookup("second").manager.Config().Enabled {
		t.Fatal("restore enabled Agent")
	}
}

func TestAccountDirectoryWriteFailureKeepsRegistration(t *testing.T) {
	f, a := setupAccounts(t)
	path := filepath.Join(f.manager.dir, "accounts.json")
	if err := os.Rename(path, path+".backup"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.request("POST", "/api/accounts/second/manage", map[string]string{"action": "remove"}, true, true); code != 500 {
		t.Fatalf("write failure: %d", code)
	}
	if a.lookup("second").record.Removed {
		t.Fatal("failed write removed runtime")
	}
}

func TestAccountRemovalRequiresPause(t *testing.T) {
	f, a := setupAccounts(t)
	m := a.lookup("second").manager
	m.mu.Lock()
	m.config.Enabled = true
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.config.Enabled = false; m.mu.Unlock() }()
	if code, _, _ := f.request("POST", "/api/accounts/second/manage", map[string]string{"action": "remove"}, true, true); code != 409 {
		t.Fatalf("running account removal: %d", code)
	}
	if a.lookup("second").record.Removed {
		t.Fatal("running account removed")
	}
}
