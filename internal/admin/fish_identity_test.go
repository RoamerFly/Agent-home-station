package admin

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fishAccountRows deliberately exercises the public account-list contract rather
// than reaching into the registry.  A removed account must not disclose a stale
// fish identity through this endpoint.
func fishAccountRows(t *testing.T, f *webFixture) map[string]string {
	t.Helper()
	code, body, _ := f.request("GET", "/api/accounts", nil, true, false)
	if code != http.StatusOK {
		t.Fatalf("list accounts: %d %s", code, body)
	}
	var response struct {
		Items []struct {
			ID     string `json:"id"`
			FishID string `json:"fish_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode account list: %v: %s", err, body)
	}
	rows := make(map[string]string, len(response.Items))
	for _, item := range response.Items {
		rows[item.ID] = item.FishID
	}
	return rows
}

func fishManage(t *testing.T, f *webFixture, account, action string, want int) string {
	t.Helper()
	code, body, _ := f.request("POST", "/api/accounts/"+account+"/manage", map[string]string{"action": action}, true, true)
	if code != want {
		t.Fatalf("%s %s: got %d, want %d: %s", action, account, code, want, body)
	}
	if want != http.StatusOK {
		return ""
	}
	var response struct {
		ID     string `json:"id"`
		FishID string `json:"fish_id"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode %s response: %v: %s", action, err, body)
	}
	if response.ID != account {
		t.Fatalf("%s response account: got %q, want %q", action, response.ID, account)
	}
	return response.FishID
}

// restartFishFixture closes the complete handler/manager stack and creates a
// fresh one on the same synthetic private directory.  It is intentionally not a
// registry-only reload: fish identity must survive a real service restart.
func restartFishFixture(t *testing.T, f *webFixture) *webFixture {
	t.Helper()
	native := f.manager.native
	dir, stateDir := f.manager.dir, f.manager.stateDir
	f.server.Close()
	if err := f.manager.Close(); err != nil {
		t.Fatalf("close before restart: %v", err)
	}
	m, err := NewManager(dir, stateDir, native)
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	h, err := NewHandler(m, "test-password-at-least-24-bytes", "")
	if err != nil {
		_ = m.Close()
		t.Fatalf("restart handler: %v", err)
	}
	srv := httptest.NewServer(h)
	restarted := &webFixture{t: t, manager: m, server: srv, client: srv.Client(), dir: f.dir}
	restarted.client.Jar, _ = cookiejar.New(nil)
	restarted.login()
	t.Cleanup(func() { srv.Close(); _ = m.Close() })
	return restarted
}

func TestFishIdentityAccountBindingLifecycle(t *testing.T) {
	f, _ := setupAccounts(t)

	// An unrecognised account cannot create an arbitrary binding record.
	fishManage(t, f, "missing", "bind_fish", http.StatusNotFound)

	fishID := fishManage(t, f, "test", "bind_fish", http.StatusOK)
	if fishID == "" {
		t.Fatal("primary bind returned an empty fish_id")
	}
	if again := fishManage(t, f, "test", "bind_fish", http.StatusOK); again != fishID {
		t.Fatalf("repeated primary bind changed fish identity: %q != %q", again, fishID)
	}
	if second := fishManage(t, f, "second", "bind_fish", http.StatusOK); second != fishID {
		t.Fatalf("secondary did not join primary fish: %q != %q", second, fishID)
	}
	rows := fishAccountRows(t, f)
	if rows["test"] != fishID || rows["second"] != fishID {
		t.Fatalf("active bindings absent from account list: %#v", rows)
	}

	// A fresh handler/manager must reconstruct the durable identity and keep
	// bind requests idempotent after restart.
	f = restartFishFixture(t, f)
	if rows = fishAccountRows(t, f); rows["test"] != fishID || rows["second"] != fishID {
		t.Fatalf("restart changed active bindings: %#v", rows)
	}
	if again := fishManage(t, f, "second", "bind_fish", http.StatusOK); again != fishID {
		t.Fatalf("repeated secondary bind after restart changed fish: %q != %q", again, fishID)
	}

	// Removed accounts must be neither bindable nor observable as active fish
	// accounts.  Restoring the private account restores its durable binding.
	fishManage(t, f, "second", "remove", http.StatusOK)
	if rows = fishAccountRows(t, f); rows["second"] != "" {
		t.Fatalf("removed account disclosed fish identity: %#v", rows)
	}
	fishManage(t, f, "second", "bind_fish", http.StatusNotFound)
	fishManage(t, f, "second", "unbind_fish", http.StatusNotFound)
	fishManage(t, f, "second", "restore", http.StatusOK)
	if rows = fishAccountRows(t, f); rows["second"] != fishID {
		t.Fatalf("restore did not recover fish binding: %#v", rows)
	}

	// Explicit unbinding clears only that account.  Once the final account is
	// unbound, a later bind must reuse the fish identity rather than minting one.
	if cleared := fishManage(t, f, "second", "unbind_fish", http.StatusOK); cleared != "" {
		t.Fatalf("secondary unbind returned fish_id %q", cleared)
	}
	if rows = fishAccountRows(t, f); rows["second"] != "" || rows["test"] != fishID {
		t.Fatalf("secondary unbind cleared the wrong binding: %#v", rows)
	}
	if cleared := fishManage(t, f, "test", "unbind_fish", http.StatusOK); cleared != "" {
		t.Fatalf("last unbind returned fish_id %q", cleared)
	}
	if rows = fishAccountRows(t, f); rows["test"] != "" || rows["second"] != "" {
		t.Fatalf("last unbind did not clear all bindings: %#v", rows)
	}
	if rebound := fishManage(t, f, "test", "bind_fish", http.StatusOK); rebound != fishID {
		t.Fatalf("rebind after final unbind changed fish identity: %q != %q", rebound, fishID)
	}

	// A different synthetic service root is an isolated fish and cannot inherit
	// either the ID or bindings from this account registry.
	other, _ := setupAccounts(t)
	otherFish := fishManage(t, other, "test", "bind_fish", http.StatusOK)
	if otherFish == "" || otherFish == fishID {
		t.Fatalf("independent service was not fish-isolated: primary=%q other=%q", fishID, otherFish)
	}
	if rows = fishAccountRows(t, other); rows["second"] != "" || rows["test"] != otherFish {
		t.Fatalf("isolated service inherited bindings: %#v", rows)
	}
}

// Binding a pair of platform accounts to one fish is presentation identity
// only. It must not cause the account selector in the management API to share
// either account's private workspace records.
func TestFishBindingDoesNotMergePrivateAdminWorkspace(t *testing.T) {
	f, _ := setupAccounts(t)
	if fishManage(t, f, "test", "bind_fish", http.StatusOK) == "" {
		t.Fatal("primary fish bind missing identity")
	}
	fishManage(t, f, "second", "bind_fish", http.StatusOK)

	accounts := []string{"test", "second"}
	for _, account := range accounts {
		content := "synthetic-private-workspace-" + account
		code, body, _ := f.request("POST", "/api/agent/workspace?account="+account, map[string]string{
			"scope": "global", "name": "fish-private.md", "content": content,
		}, true, true)
		if code != http.StatusOK {
			t.Fatalf("write private workspace for %s: %d %s", account, code, body)
		}
	}
	// Read after both writes: a shared workspace would otherwise pass the first
	// account's immediate read and only overwrite it on the second write.
	for _, account := range accounts {
		content := "synthetic-private-workspace-" + account
		code, body, _ := f.request("GET", "/api/agent/workspace?account="+account+"&scope=global&name=fish-private.md", nil, true, false)
		if code != http.StatusOK || !strings.Contains(string(body), content) {
			t.Fatalf("read own private workspace for %s: %d %s", account, code, body)
		}
		other := "test"
		if account == other {
			other = "second"
		}
		if strings.Contains(string(body), "synthetic-private-workspace-"+other) {
			t.Fatalf("fish binding merged private workspace into %s: %s", account, body)
		}
	}
}

func TestFishPurgePreservesOtherBindingAndTombstoneCannotBind(t *testing.T) {
	f, _ := setupAccounts(t)
	fishID := fishManage(t, f, "test", "bind_fish", http.StatusOK)
	if second := fishManage(t, f, "second", "bind_fish", http.StatusOK); second != fishID {
		t.Fatalf("secondary fish mismatch: %q != %q", second, fishID)
	}
	fishManage(t, f, "second", "remove", http.StatusOK)
	fishManage(t, f, "second", "purge", http.StatusOK)
	rows := fishAccountRows(t, f)
	if rows["test"] != fishID || rows["second"] != "" {
		t.Fatalf("purge changed remaining fish binding or disclosed tombstone: %#v", rows)
	}
	for _, action := range []string{"bind_fish", "unbind_fish"} {
		fishManage(t, f, "second", action, http.StatusNotFound)
	}
	// Native discovery continues returning "second" in this synthetic fixture;
	// the durable tombstone must still stop it becoming bound again.
	if rows = fishAccountRows(t, f); rows["second"] != "" || rows["test"] != fishID {
		t.Fatalf("native refresh resurrected purged fish binding: %#v", rows)
	}
}

func TestFishIdentityCorruptionAndWriteAuthentication(t *testing.T) {
	f, _ := setupAccounts(t)
	// The route remains protected even though fish binding does not send a
	// WeChat message. An anonymous client and a session without CSRF both fail.
	authenticated := f.client
	f.client = &http.Client{}
	if code, _, _ := f.request("POST", "/api/accounts/test/manage", map[string]string{"action": "bind_fish"}, true, false); code != http.StatusUnauthorized {
		t.Fatalf("anonymous fish bind: %d", code)
	}
	f.client = authenticated
	if code, _, _ := f.request("POST", "/api/accounts/test/manage", map[string]string{"action": "bind_fish"}, true, false); code != http.StatusForbidden {
		t.Fatalf("fish bind without CSRF: %d", code)
	}

	// Never silently replace a corrupt binding registry with a fresh fish.
	path := filepath.Join(f.manager.dir, "fish-identity.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"fish_id":"fish-not-hex","bindings":["test"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.request("POST", "/api/accounts/test/manage", map[string]string{"action": "bind_fish"}, true, true); code != http.StatusInternalServerError {
		t.Fatalf("corrupt fish registry bind: %d", code)
	}
	if code, body, _ := f.request("GET", "/api/accounts", nil, true, false); code != http.StatusOK || !strings.Contains(string(body), "fish_error") {
		t.Fatalf("corrupt fish registry was silently shown as unbound: %d %s", code, body)
	}
}
