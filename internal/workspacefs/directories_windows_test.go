//go:build windows

package workspacefs

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWindowsJunctionCannotRedirectManagedDirectory(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	link := filepath.Join(dir, "junction")
	cmd := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, outside)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("test junction unavailable: %v %s", err, output)
	}
	if root, err := EnsurePath(filepath.Join(link, "must-not-exist")); err == nil {
		root.Close()
		t.Fatal("junction accepted as managed ancestor")
	}
	if _, err := os.Lstat(filepath.Join(outside, "must-not-exist")); !os.IsNotExist(err) {
		t.Fatal("junction created directory outside grant", err)
	}
}
