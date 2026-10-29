package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceReadableNameIsStableAndPrivateLibraryBelongsToAccount(t *testing.T) {
	f := setup(t)
	m := f.manager
	name := filepath.Base(m.privateWorkspaceFor(m.native.Account()))
	if len(name) == 64 || !strings.Contains(name, "--") {
		t.Fatal("new workspace still opaque", name)
	}
	if m.privateVoiceLibrary() != filepath.Join(m.privateWorkspaceFor(m.native.Account()), "voice-library") {
		t.Fatal("private voices outside account workspace")
	}
	if err := m.configureWorkspaceDirectory(); err != nil || filepath.Base(m.privateWorkspaceFor(m.native.Account())) != name {
		t.Fatal("workspace identity changed", err)
	}
	if err := os.WriteFile(filepath.Join(m.privateWorkspaceFor(m.native.Account()), "account.json"), []byte(`{"account_id":"foreign","scope":"wrong"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.writeWorkspaceIdentity(); err == nil {
		t.Fatal("foreign account metadata overwritten")
	}
}

func TestWorkspaceDirectoryMarkerCannotEscape(t *testing.T) {
	f := setup(t)
	for _, name := range []string{"..", "../foreign", `C:\private`, "/root", "x/y", "x\x00"} {
		if validWorkspaceDirectory(name) {
			t.Fatal("unsafe name accepted", name)
		}
	}
	if err := writePrivate(filepath.Join(f.manager.dir, "workspace-directory"), []byte("../foreign")); err != nil {
		t.Fatal(err)
	}
	if f.manager.configureWorkspaceDirectory() == nil {
		t.Fatal("unsafe marker loaded")
	}
}
