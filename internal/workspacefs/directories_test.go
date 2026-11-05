package workspacefs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDirectoryCreationAndAncestorLinks(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	root, err := EnsurePath(filepath.Join(dir, "new", "nested"))
	if err != nil {
		t.Fatal(err)
	}
	root.Close()
	if err := os.Symlink(outside, filepath.Join(dir, "alias")); err != nil {
		t.Skip("symlink creation unavailable", err)
	}
	if root, err := EnsurePath(filepath.Join(dir, "alias", "must-not-exist")); err == nil {
		root.Close()
		t.Fatal("linked ancestor accepted")
	}
	if _, err := os.Lstat(filepath.Join(outside, "must-not-exist")); !os.IsNotExist(err) {
		t.Fatal("created outside before checking ancestor", err)
	}
}

func TestDirectoryReplacementAfterStatRejectsDifferentInode(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"slot", "other"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	before, err := root.Lstat("slot")
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Rename("slot", "original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("other", filepath.Join(dir, "slot")); err != nil {
		t.Skip("symlink creation unavailable", err)
	}
	if child, err := openChildDirectory(root, "slot", before); !errors.Is(err, ErrDirectoryChanged) {
		if child != nil {
			child.Close()
		}
		t.Fatal("changed directory accepted", err)
	}
}

func TestLiveExistingDirectoryIdentity(t *testing.T) {
	path := os.Getenv("WECHAT_LIVE_DIRECTORY_ROOT")
	if path == "" {
		t.Skip("explicit existing-root filesystem compatibility check")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		t.Fatal("expected an existing real directory", err)
	}
	root, err := EnsurePath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f, err := OpenDirectory(root, ".")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) {
		t.Fatal("filesystem directory identity mismatch", err)
	}
	t.Log("existing-root identity verified; no file contents read or changed")
}
