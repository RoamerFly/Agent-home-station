package workspacefs

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestRootedTypedOpenAndDirectoryReplacement(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plain"), []byte("synthetic-original"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f, err := OpenRegular(root, "plain")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil || string(data) != "synthetic-original" {
		t.Fatal(string(data), err)
	}
	if _, err := OpenRegular(root, "."); !errors.Is(err, ErrNotRegular) {
		t.Fatal("directory accepted as file", err)
	}
	if _, err := OpenDirectory(root, "plain"); !errors.Is(err, ErrNotDirectory) {
		t.Fatal("file accepted as directory", err)
	}
	f, err = OpenDirectory(root, ".")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := OpenRegular(root, "../outside"); err == nil {
		t.Fatal("parent traversal accepted")
	}
}

func TestOpenSymlinkBoundaryAndPinnedRoot(t *testing.T) {
	base := t.TempDir()
	dir, other := filepath.Join(base, "grant"), filepath.Join(base, "outside")
	for _, d := range []string{dir, other} {
		if err := os.Mkdir(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(dir, "plain"), []byte("inside"), 0600)
	os.WriteFile(filepath.Join(other, "plain"), []byte("outside"), 0600)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Symlink(filepath.Join(other, "plain"), filepath.Join(dir, "escape")); err != nil {
		t.Skip("symlink creation unavailable", err)
	}
	if _, err := OpenRegular(root, "escape"); err == nil {
		t.Fatal("external symlink accepted")
	}
	if err := os.Symlink("plain", filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	f, err := OpenRegular(root, "alias")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.Rename(dir, dir+"-original"); err != nil {
		t.Skip("root rename unavailable", err)
	}
	if err := os.Symlink(other, dir); err != nil {
		t.Fatal(err)
	}
	f, err = OpenRegular(root, "plain")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil || string(data) != "inside" {
		t.Fatal("root followed replacement", string(data), err)
	}
}
