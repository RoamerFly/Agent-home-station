package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"wechat-bot/internal/companion"
)

func TestManagedNotesRejectAncestorDirectoryLink(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(outside, "memory", "secret.md"), []byte("SYNTHETIC_OUTSIDE_SCOPE"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(global, global+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, global); err != nil {
		t.Skip("symlink creation unavailable", err)
	}
	result, err := p.Call(context.Background(), "workspace_read", json.RawMessage(`{"global":true,"name":"secret.md"}`))
	if err == nil || strings.Contains(result, "SYNTHETIC_OUTSIDE_SCOPE") {
		t.Fatalf("ancestor link escaped notes grant: result=%s err=%v", result, err)
	}
}

func TestEnsureManagedDoesNotCreateThroughAncestorLink(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	link := filepath.Join(base, "alias")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("symlink creation unavailable", err)
	}
	if err := ensureManaged(filepath.Join(link, "new-directory")); err == nil {
		t.Fatal("linked directory accepted")
	}
	if _, err := os.Lstat(filepath.Join(outside, "new-directory")); !os.IsNotExist(err) {
		t.Fatal("directory was created outside grant before validation", err)
	}
}

func TestManagerNormalizesRelativeStoragePaths(t *testing.T) {
	t.Setenv("WECHAT_WORKSPACE_ROOT", "")
	t.Chdir(t.TempDir())
	m, err := NewManager("relative-control", "relative-state", &fakeNative{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if !filepath.IsAbs(m.dir) || !filepath.IsAbs(m.stateDir) || !filepath.IsAbs(m.managedRoot()) {
		t.Fatal("relative path persisted into execution configuration")
	}
}

func TestIncomingFileForeignAccountFailsBeforeDownloadOrDirectoryCreation(t *testing.T) {
	f, p, n := forwardFixture(t)
	msg := n.message
	msg.AccountID = "foreign-account"
	if _, err := (mediaUnderstanding{manager: f.manager}).receiveFile(context.Background(), msg, "notes.txt"); err == nil {
		t.Fatal("foreign file accepted")
	}
	if n.downloads != 0 {
		t.Fatal("foreign file downloaded")
	}
	other := filepath.Join(filepath.Dir(p.executionContext().Private), accountDirectory(msg.AccountID), "conversations", companion.ScopeID("wechat", msg.AccountID, msg.ThreadID))
	if _, err := os.Lstat(other); !os.IsNotExist(err) {
		t.Fatal("foreign account directory was created", err)
	}
}
