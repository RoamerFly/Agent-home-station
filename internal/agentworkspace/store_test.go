package agentworkspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceHasNo256DocumentQuota(t *testing.T) {
	base := t.TempDir()
	s, e := Open(base)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	s.Read("shared", "init.md")
	for i := 0; i < 256; i++ {
		if e := os.WriteFile(filepath.Join(base, "shared", fmt.Sprintf("note-%03d.md", i)), []byte("synthetic"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = s.Write("shared", "note-256.md", "new note", ""); e != nil {
		t.Fatal("old quota retained", e)
	}
	docs, e := s.List("shared", "")
	if e != nil || len(docs) != 257 {
		t.Fatal("list truncated", len(docs), e)
	}
}

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
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Read(scope, "MEMORY.md"); !errors.Is(e, os.ErrClosed) {
		t.Fatal("closed store accepted read", e)
	}
	s, _ = Open(root)
	defer s.Close()
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

func TestWorkspaceIndexAndPageSurviveMalformedNote(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Read("shared", "prepare.md"); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"a-broken.md": strings.Repeat("x", 65537),
		"b-good.md":   "普通笔记",
		"c-tail.md":   strings.Repeat("前", 600) + "目标词在结尾",
	} {
		if err := os.WriteFile(filepath.Join(root, "shared", name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	names, total, err := s.Names("shared", 2)
	if err != nil || total != 3 || len(names) != 2 || names[0] != "a-broken.md" {
		t.Fatal(names, total, err)
	}
	page, err := s.Page("shared", "", 0, 2)
	if err != nil || page.Total != 3 || len(page.Documents) != 2 || page.Documents[0].Name != "a-broken.md" || page.Documents[0].Revision != "" || page.Documents[1].Content != "" || len(page.Warnings) != 1 {
		t.Fatal(page, err)
	}
	search, err := s.Page("shared", "目标词", 0, 1)
	if err != nil || search.Total != 1 || len(search.Documents) != 1 || search.Documents[0].Name != "c-tail.md" || !strings.Contains(search.Documents[0].Content, "目标词") || len(search.Warnings) != 1 {
		t.Fatal(search, err)
	}
}
