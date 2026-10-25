package historyarchive

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestArchiveRoundTripDeduplicatesAndRejectsTampering(t *testing.T) {
	s := Store{Root: filepath.Join(t.TempDir(), "archive")}
	scope := strings.Repeat("a", 64)
	i, err := s.Put("memory", scope, map[string]string{"text": "合成私有历史"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Put("memory", scope, map[string]string{"text": "合成私有历史"})
	if err != nil || again.ID != i.ID {
		t.Fatal(again, err)
	}
	items, next, err := s.List("memory", scope, "", 20)
	if err != nil || len(items) != 1 || next != "" {
		t.Fatal(items, next, err)
	}
	b, _, err := s.Read("memory", scope, i.ID)
	if err != nil || !bytes.Contains(b.Data, []byte("合成私有历史")) {
		t.Fatal(b, err)
	}
	path := filepath.Join(s.Root, "memory", scope, i.ID+".json.gz")
	st, _ := os.Stat(path)
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
	var corrupt bytes.Buffer
	z := gzip.NewWriter(&corrupt)
	z.Write([]byte(`{"version":1,"kind":"memory","scope":"wrong","data":{}}`))
	z.Close()
	if err = os.WriteFile(path, corrupt.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Read("memory", scope, i.ID); err == nil {
		t.Fatal("corrupted archive accepted")
	}
	if _, err = s.Put("memory", scope, map[string]string{"text": "合成私有历史"}); err == nil {
		t.Fatal("corrupted existing archive overwritten")
	}
}
func TestArchivePaginationAndScopeIsolation(t *testing.T) {
	s := Store{Root: t.TempDir()}
	scope := strings.Repeat("b", 64)
	for i := 0; i < 5; i++ {
		if _, err := s.Put("activity", scope, i); err != nil {
			t.Fatal(err)
		}
	}
	one, next, err := s.List("activity", scope, "", 2)
	if err != nil || len(one) != 2 || next == "" {
		t.Fatal(one, next, err)
	}
	two, _, err := s.List("activity", scope, next, 2)
	if err != nil || len(two) != 2 || two[0].ID == one[0].ID {
		t.Fatal(two, err)
	}
	if _, _, err = s.Read("activity", strings.Repeat("c", 64), one[0].ID); err == nil {
		t.Fatal("cross scope read")
	}
	if _, err := s.Put("../escape", scope, 1); err == nil {
		t.Fatal("unsafe kind")
	}
	if _, _, err = s.Read("activity", scope, "../escape"); err == nil {
		t.Fatal("unsafe ID")
	}
	if _, err := s.Put("memory", scope, strings.Repeat("x", MaxPayload)); err == nil {
		t.Fatal("oversized archive")
	}
}
func TestArchiveNeverFollowsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows developer mode not required")
	}
	root, other := t.TempDir(), t.TempDir()
	scope := strings.Repeat("d", 64)
	if err := os.Symlink(other, filepath.Join(root, "memory")); err != nil {
		t.Fatal(err)
	}
	s := Store{Root: root}
	if _, err := s.Put("memory", scope, 1); err == nil {
		t.Fatal("symlink archive accepted")
	}
	entries, _ := os.ReadDir(other)
	if len(entries) != 0 {
		t.Fatal("wrote outside archive")
	}
}
