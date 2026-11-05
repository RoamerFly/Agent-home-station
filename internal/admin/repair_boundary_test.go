package admin

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestProductionAdminDoesNotRegisterDeveloperRepairAcceptance(t *testing.T) {
	f := setup(t)
	retained := filepath.Join(f.dir, "repair-test-runs", "synthetic-retained.json")
	if err := os.MkdirAll(filepath.Dir(retained), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"synthetic":"retained original record"}`)
	if err := os.WriteFile(retained, original, 0600); err != nil {
		t.Fatal(err)
	}
	f.login()
	for _, path := range []string{"/api/agent/repair-tests", "/api/agent/repair-tests/synthetic/followup", "/api/agent/repair-tests/synthetic/lifecycle"} {
		if code, _, _ := f.request("POST", path, map[string]string{}, true, true); code != 404 {
			t.Fatal("developer acceptance route is present in production", path, code)
		}
	}
	if code, _, _ := f.request("GET", "/api/agent/repair-tests/synthetic-retained", nil, false, false); code != 404 {
		t.Fatal("developer acceptance read route is present in production", code)
	}
	if data, err := os.ReadFile(retained); err != nil || !bytes.Equal(data, original) {
		t.Fatal("removing the route changed the retained record", err)
	}
}
