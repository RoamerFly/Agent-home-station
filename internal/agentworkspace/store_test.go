package agentworkspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNotesPersistAndConflictWithoutCrossScopeAccess(t *testing.T) {
	root := t.TempDir()
	s, e := Open(root)
	if e != nil {
		t.Fatal(e)
	}
	scope := strings.Repeat("a", 64)
	d, e := s.Write(scope, "MEMORY.md", "# 项目经验\n工具成功不等于消息送达。", "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Write(scope, d.Name, "lost", ""); !errors.Is(e, ErrConflict) {
		t.Fatal("overwrite without revision", e)
	}
	s, _ = Open(root)
	read, e := s.Read(scope, d.Name)
	if e != nil || read.Content != d.Content || read.Revision != d.Revision {
		t.Fatal(read, e)
	}
	if _, e = s.Write(scope, d.Name, "# 已更新", d.Revision); e != nil {
		t.Fatal(e)
	}
	other, e := s.List(strings.Repeat("b", 64), "")
	if e != nil || len(other) != 0 {
		t.Fatal("cross-scope notes", other, e)
	}
	for _, name := range []string{"../secret.md", "/absolute.md", "x\\y.md", "notes.txt"} {
		if _, e = s.Write(scope, name, "bad", ""); e == nil {
			t.Fatal(name)
		}
	}
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.md"), []byte("private"), 0600)
	if e = os.Symlink(outside, filepath.Join(root, "shared")); e == nil {
		if _, e = s.Read("shared", "secret.md"); e == nil {
			t.Fatal("namespace symlink escaped")
		}
	}
}
