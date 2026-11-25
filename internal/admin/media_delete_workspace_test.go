package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestMediaDeletionScansStructuredAndLegacyHomesOnly(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	adminDir := filepath.Join(data, "applications", "wechat", "legacy-admin")
	if err := os.MkdirAll(adminDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(data, "workspaces"), 0700); err != nil {
		t.Fatal(err)
	}
	m := &Manager{dir: adminDir, native: &fakeNative{}}
	content := []byte("synthetic media for workspace deletion")
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	paths := []string{
		filepath.Join(data, "workspaces", "home", "new.wav"),
		filepath.Join(data, "workspace", "home", "old.wav"),
		filepath.Join(data, "workspaces", "douyin", "private.wav"),
		filepath.Join(data, "main_setting", "secret.wav"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	preview, err := m.deleteMediaResources(t.Context(), []string{hash}, true)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, path := range preview.Files {
		found[path] = true
	}
	if !found[paths[0]] || !found[paths[1]] || found[paths[2]] || found[paths[3]] {
		t.Fatal(found)
	}
	result, err := m.deleteMediaResources(t.Context(), []string{hash}, false)
	if err != nil || len(result.Failures) != 0 {
		t.Fatal(result, err)
	}
	for i, path := range paths {
		_, err := os.Stat(path)
		if i < 2 && !os.IsNotExist(err) || i >= 2 && err != nil {
			t.Fatal(path, err)
		}
	}
}
