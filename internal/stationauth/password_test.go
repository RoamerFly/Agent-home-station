package stationauth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPasswordPrecedenceAndCompatibility(t *testing.T) {
	dir := t.TempDir()
	check := func(explicit, want string) {
		t.Helper()
		if got, err := Password(dir, explicit); err != nil || (want != "" && got != want) {
			t.Fatalf("credential selection failed (match=%t): %v", got == want, err)
		}
	}
	write := func(name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	check("synthetic-explicit", "synthetic-explicit")
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("environment override wrote a credential: %v", err)
	}
	check("", DefaultPassword)
	check("", DefaultPassword)
	write("admin-token", "admin\r\n")
	check("", DefaultPassword)
	if err := SavePassword(dir, "synthetic-new-password"); err != nil {
		t.Fatal(err)
	}
	check("", "synthetic-new-password")
	if err := SavePassword(dir, "admin"); err != nil {
		t.Fatal(err)
	}
	check("", "admin")
	if err := SavePassword(dir, "bad"); err == nil {
		t.Fatal("invalid password saved")
	}
	check("", "admin")
	if err := os.Remove(filepath.Join(dir, "admin-password")); err != nil {
		t.Fatal(err)
	}
	const oldToken = "synthetic-legacy-token\r\n"
	write("admin-token", oldToken)
	check("", "synthetic-legacy-token")
	check("synthetic-explicit", "synthetic-explicit")
	const managed = "synthetic-managed-owner\r\n"
	write("admin-password", managed)
	check("", "synthetic-managed-owner")
	check("synthetic-explicit", "synthetic-managed-owner")
	check(" invalid-explicit ", "synthetic-managed-owner")
	for name, want := range map[string]string{"admin-token": oldToken, "admin-password": managed} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != want {
			t.Fatalf("existing %s was modified: %v", name, err)
		}
	}
}

func TestPasswordRejectsUnsafeCredentials(t *testing.T) {
	dir := t.TempDir()
	tooLong := strings.Repeat("a", maxPasswordFile+1)
	if err := os.WriteFile(filepath.Join(dir, "admin-password"), []byte(tooLong), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := Password(dir, "synthetic-fallback"); got != "" || err == nil {
		t.Fatalf("oversized credential was accepted")
	}
	if err := os.Remove(filepath.Join(dir, "admin-password")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "admin-token"), 0700); err != nil {
		t.Fatal(err)
	}
	// An explicit environment override still wins over a damaged file, but the
	// stored path must never be accepted as a credential source.
	if got, err := Password(dir, ""); got != "" || err == nil {
		t.Fatalf("non-regular credential was accepted")
	}
}
