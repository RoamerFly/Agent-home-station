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
	// A first start must not publish a fixed password, and repeated starts
	// must keep the same generated value.
	first, err := Password(dir, "")
	if err != nil {
		t.Fatalf("bootstrap credential failed: %v", err)
	}
	if first == legacyDefaultPassword || len(first) < 32 {
		t.Fatalf("bootstrap credential is not random")
	}
	if again, err := Password(dir, ""); err != nil || again != first {
		t.Fatalf("bootstrap credential was not stable: %v", err)
	}
	// A legacy fixed token is rotated exactly once without touching the old
	// file, so older readers keep working.
	write("admin-token", legacyDefaultPassword+"\r\n")
	rotated, err := Password(dir, "")
	if err != nil || rotated == legacyDefaultPassword || rotated == first {
		t.Fatalf("legacy default credential was not rotated: %v", err)
	}
	if again, err := Password(dir, ""); err != nil || again != rotated {
		t.Fatalf("rotated credential was not stable: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "admin-token")); err != nil || string(raw) != legacyDefaultPassword+"\r\n" {
		t.Fatalf("legacy token file was modified: %v", err)
	}
	const oldToken = "synthetic-legacy-token\r\n"
	if err := os.Remove(filepath.Join(dir, "admin-password")); err != nil {
		t.Fatal(err)
	}
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
