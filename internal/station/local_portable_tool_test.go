package station

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPortablePathsStayInsideToolPackage(t *testing.T) {
	dir := t.TempDir()
	relative := "models"
	if err := os.Mkdir(filepath.Join(dir, relative), 0700); err != nil {
		t.Fatal(err)
	}
	if err := resolvePortablePaths(dir, &relative); err != nil || relative != filepath.Join(dir, "models") {
		t.Fatalf("resolve portable path: %q, %v", relative, err)
	}
	for _, path := range []string{"../models", "runtime/../../outside", "C:/models", "missing"} {
		if err := resolvePortablePaths(dir, &path); err == nil {
			t.Fatalf("unexpectedly accepted %q", path)
		}
	}
}

func TestPortableRegistrationsKeepExplicitPrivatePreferences(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "owner-registration.json")
	original := []byte(`{"enabled":false}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	raw, portable, err := portableRegistration(path, "", localAudioCppPackage, filepath.Join(dir, "workspace"), 4096)
	if err != nil || string(raw) != string(original) || portable != "" {
		t.Fatalf("private preference was ignored: %v", err)
	}
}
