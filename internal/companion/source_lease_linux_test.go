//go:build linux

package companion

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKnownProcPseudoHandle(t *testing.T) {
	tests := []struct {
		target string
		want   bool
	}{
		{target: "socket:[123]", want: true},
		{target: "pipe:[42]", want: true},
		{target: "anon_inode:[eventfd]", want: true},
		{target: "/tmp/memory.sqlite", want: false},
		{target: "/tmp/other-file (deleted)", want: false},
		// Only the documented pseudo-handle kinds can be skipped. Unknown
		// targets, including anonymous memfds, remain fail-closed.
		{target: "memfd:unknown", want: false},
		{target: "", want: false},
	}

	for _, test := range tests {
		if got := isKnownProcPseudoHandle(test.target); got != test.want {
			t.Errorf("isKnownProcPseudoHandle(%q) = %t, want %t", test.target, got, test.want)
		}
	}
}

func TestVerifyLegacyMemoryOfflineDetectsHardLinkedDescriptor(t *testing.T) {
	dir := t.TempDir()
	memoryPath := filepath.Join(dir, "memory.sqlite")
	if err := os.WriteFile(memoryPath, []byte("memory"), 0600); err != nil {
		t.Fatal(err)
	}
	aliasPath := filepath.Join(dir, "memory-alias.sqlite")
	if err := os.Link(memoryPath, aliasPath); err != nil {
		t.Fatal(err)
	}

	procRoot := filepath.Join(dir, "proc")
	fdDir := filepath.Join(procRoot, "123", "fd")
	if err := os.MkdirAll(fdDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(aliasPath, filepath.Join(fdDir, "7")); err != nil {
		t.Fatal(err)
	}

	err := verifyLegacyMemoryOfflineInProc(memoryPath, procRoot, os.Getuid())
	if err == nil {
		t.Fatal("hard-linked descriptor was not detected")
	}
	if got, want := err.Error(), "memory database is open in another process; stop all Web instances before offline repair"; got != want {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVerifyLegacyMemoryOfflineSkipsKnownPseudoHandle(t *testing.T) {
	dir := t.TempDir()
	memoryPath := filepath.Join(dir, "memory.sqlite")
	if err := os.WriteFile(memoryPath, []byte("memory"), 0600); err != nil {
		t.Fatal(err)
	}

	procRoot := filepath.Join(dir, "proc")
	fdDir := filepath.Join(procRoot, "123", "fd")
	if err := os.MkdirAll(fdDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socket:[123]", filepath.Join(fdDir, "7")); err != nil {
		t.Fatal(err)
	}

	if err := verifyLegacyMemoryOfflineInProc(memoryPath, procRoot, os.Getuid()); err != nil {
		t.Fatal(err)
	}
}
