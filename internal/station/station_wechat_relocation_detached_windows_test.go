package station

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyDetachedFenceRejectsDirectoryCopyAtSamePath(t *testing.T) {
	base := t.TempDir()
	active := filepath.Join(base, "receiver")
	if err := os.Mkdir(active, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(active)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := sealLegacyDetachedFence(root, "synthetic-relocation-fence")
	if err != nil {
		root.Close()
		t.Fatal(err)
	}
	digest, err := openLegacyDetachedFence(root, ciphertext)
	root.Close()
	if err != nil || digest != "synthetic-relocation-fence" {
		t.Fatal("machine did not verify its own seal", err)
	}
	if err := os.Rename(active, filepath.Join(base, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(active, 0700); err != nil {
		t.Fatal(err)
	}
	replacement, err := os.OpenRoot(active)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if _, err := openLegacyDetachedFence(replacement, ciphertext); err == nil {
		t.Fatal("a new directory at the original pathname accepted the seal")
	}
}
