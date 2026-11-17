package pluginhost

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogSnapshotExcludesModelAndRejectsTamper(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "models"), 0700)
	os.WriteFile(filepath.Join(src, "models", "large.bin"), []byte("private model"), 0600)
	code := []byte("print('safe')")
	hash := sha256.Sum256(code)
	raw := []byte(`{"files":{"main.py":"` + hex.EncodeToString(hash[:]) + `"}}`)
	sum := sha256.Sum256(raw)
	pin := hex.EncodeToString(sum[:])
	os.WriteFile(filepath.Join(src, "plugin.json"), raw, 0600)
	os.WriteFile(filepath.Join(src, "main.py"), code, 0600)
	dst := t.TempDir()
	if err := snapshotCatalog(t.Context(), src, dst, pin); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "models")); !os.IsNotExist(err) {
		t.Fatal("model copied")
	}
	os.WriteFile(filepath.Join(src, "plugin.json"), []byte(`{}`), 0600)
	if snapshotCatalog(t.Context(), src, t.TempDir(), pin) == nil {
		t.Fatal("changed manifest accepted")
	}
}
