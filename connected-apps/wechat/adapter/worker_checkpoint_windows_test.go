//go:build windows

package adapter

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWorkerCheckpointDoesNotFollowForeignQueue(t *testing.T) {
	foreign := t.TempDir()
	if err := os.WriteFile(filepath.Join(foreign, "messages-v2.json"), []byte(workerCheckpointFixture), 0600); err != nil {
		t.Fatal(err)
	}
	runtimeDir := t.TempDir()
	cmd := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", filepath.Join(runtimeDir, "queue"), foreign)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("test junction unavailable: %v %s", err, output)
	}
	if _, err := WorkerCheckpointAccount(runtimeDir, ConnectionID("fixture")); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatal("redirected queue was read or treated as a missing checkpoint", err)
	}
	if _, _, err := ReadWorkerDelivery(runtimeDir, "fixture", "original-key"); err == nil {
		t.Fatal("receipt lookup followed a redirected queue")
	}
}
