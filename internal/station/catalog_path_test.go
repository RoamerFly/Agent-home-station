package station

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogPackageDirResolvesPurposeSuffix(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "yue-作曲")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"yue"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := catalogPackageDir(root, "yue")
	if err != nil || got != dir {
		t.Fatalf("catalogPackageDir() = %q, %v; want %q", got, err, dir)
	}
}
