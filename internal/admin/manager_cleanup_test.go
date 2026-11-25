package admin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewManagerConstructionFailureReleasesWindowsRuntimeLocks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "audit.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := NewManager(dir, "", &fakeNative{}); err == nil {
		t.Fatal("corrupt audit unexpectedly allowed manager construction")
	}
	if err := os.Remove(filepath.Join(dir, "audit.json")); err != nil {
		t.Fatal(err)
	}

	m, err := NewManager(dir, "", &fakeNative{})
	if err != nil {
		t.Fatalf("manager could not restart after construction failure: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
}
