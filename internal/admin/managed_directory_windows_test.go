//go:build windows

package admin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWindowsNotesRejectAncestorJunction(t *testing.T) {
	f, p := trustedAgent(t)
	f.manager.config.Companion.Permissions.Remember = true
	c := f.manager.agent.config()
	c.CrossConversation = true
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(p.executionContext().Private, "global")
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "memory"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "memory", "secret.md"), []byte("synthetic outside notes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(global, global+"-original"); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", global, outside)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("test junction unavailable: %v %s", err, output)
	}
	if result, err := p.Call(context.Background(), "workspace_read", json.RawMessage(`{"global":true,"name":"secret.md"}`)); err == nil {
		t.Fatal("junction escaped notes grant", result)
	}
}
