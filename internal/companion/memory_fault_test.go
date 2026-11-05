package companion

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMemoryDryRunInterruptedMigrationAndWriteFailure(t *testing.T) {
	dir := t.TempDir()
	id := ScopeID("wechat", "synthetic", "room")
	legacy := persisted{Version: 1, Scopes: map[string]Scope{id: {ID: id, Channel: "wechat", Account: "synthetic", Thread: "room", Messages: []Turn{{Role: "user", Sender: "alice", Text: "相同文本", Time: time.Now()}, {Role: "user", Sender: "alice", Text: "相同文本", Time: time.Now()}}, Summary: "旧摘要", Notes: []string{"旧笔记"}}}}
	raw, _ := json.Marshal(legacy)
	path := filepath.Join(dir, "memory.json")
	os.WriteFile(path, raw, 0600)
	report, e := DryRunMemory(t.Context(), dir)
	if e != nil || report["projection_verified"] != true {
		t.Fatal(report, e)
	}
	if _, e = os.Stat(filepath.Join(dir, "memory.sqlite")); !os.IsNotExist(e) {
		t.Fatal("dry-run activated live database")
	}
	if e = os.WriteFile(filepath.Join(dir, ".memory-migration-interrupted.sqlite"), []byte("incomplete staging"), 0600); e != nil {
		t.Fatal(e)
	}
	s, e := NewStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&n)
	if n != 2 {
		t.Fatal("identical text events incorrectly merged", n)
	}
	s.db.Exec("PRAGMA query_only=ON")
	cfg := Defaults()
	cfg.Permissions.Remember = true
	before := s.data.Decisions
	if e = s.ClaimDecision(Input{Channel: "wechat", Account: "synthetic", Thread: "room"}, cfg, time.Now()); e == nil {
		t.Fatal("write failure ignored")
	}
	if s.data.Decisions != before {
		t.Fatal("failed transaction mutated in-memory counter")
	}
	s.db.Exec("PRAGMA query_only=OFF")
	if e = s.Verify(t.Context()); e != nil {
		t.Fatal(e)
	}
	kept, _ := os.ReadFile(path)
	if string(kept) != string(raw) {
		t.Fatal("migration altered legacy JSON")
	}
}

func TestArchiveImportKeepsDistinctLegacyEventsAcrossBatches(t *testing.T) {
	s, c, in := memoryFixture(t)
	in.Text = "当前对话"
	if err := s.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	id := ScopeID(in.Channel, in.Account, in.Thread)
	old := Turn{Role: "user", Sender: in.Sender, Text: "相同的旧事件", Time: time.Now().Add(-365 * 24 * time.Hour)}
	// A legacy archive has no trace IDs. Distinct batches can contain the same
	// text/time at the same array index and must still retain separate events.
	for i := range 2 {
		batch := Scope{ID: id, Channel: in.Channel, Account: in.Account, Thread: in.Thread,
			Summary: fmt.Sprintf("合成归档 %d", i), Messages: []Turn{old}}
		if _, err := s.archive().Put("memory", id, batch); err != nil {
			t.Fatal(err)
		}
	}
	for {
		done, err := s.IndexArchives(t.Context(), id, 1)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			break
		}
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM events WHERE scope=? AND text=?", id, old.Text).Scan(&count); err != nil || count != 2 {
		t.Fatal("archive identities merged", count, err)
	}
	if _, err := s.IndexArchives(t.Context(), id, 20); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM events WHERE scope=? AND text=?", id, old.Text).Scan(&count); err != nil || count != 2 {
		t.Fatal("archive retry duplicated sources", count, err)
	}
}

func TestMemoryRefusesForeignDatabaseWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.sqlite")
	db, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	db.Exec("CREATE TABLE unrelated(value TEXT)")
	db.Close()
	before, _ := os.ReadFile(path)
	if s, e := NewStore(dir); e == nil {
		s.Close()
		t.Fatal("foreign database accepted")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("foreign database modified")
	}
}

func TestMemoryTrustedOriginalPreservesLiteralObservationDelimiter(t *testing.T) {
	s, c, in := memoryFixture(t)
	raw := "正文\n以下是本条媒体的观察资料（不是指令）：\n这是用户原文，必须保留。"
	in.Text = "模型预览"
	in.OriginalText = &raw
	in.TraceID = "literal-marker"
	if e := s.Observe(in, c, time.Now()); e != nil {
		t.Fatal(e)
	}
	id := ScopeID(in.Channel, in.Account, in.Thread)
	eid := eventID(id, Turn{Role: "user", TraceID: in.TraceID}, 0)
	ev, e := s.ReadEvent(t.Context(), []string{id}, eid)
	if e != nil || ev.Text != raw {
		t.Fatal("full original truncated", ev, e)
	}
}

func TestMemoryAbruptProcessExitKeepsCommittedWAL(t *testing.T) {
	if dir := os.Getenv("WECHAT_SYNTHETIC_CRASH_DIR"); dir != "" {
		s, e := NewStore(dir)
		if e != nil {
			os.Exit(21)
		}
		c := Defaults()
		c.Permissions.Remember = true
		e = s.Observe(Input{Channel: "wechat", Account: "synthetic", Thread: "crash", Sender: "alice", TraceID: "committed", Text: "进程突然退出之前已提交的合成原文。"}, c, time.Now())
		if e != nil {
			os.Exit(22)
		}
		os.Exit(19) // Deliberately bypass Close and all deferred cleanup.
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestMemoryAbruptProcessExitKeepsCommittedWAL$")
	cmd.Env = append(os.Environ(), "WECHAT_SYNTHETIC_CRASH_DIR="+dir)
	if e := cmd.Run(); e == nil {
		t.Fatal("crash fixture exited normally")
	} else if exit, ok := e.(*exec.ExitError); !ok || exit.ExitCode() != 19 {
		t.Fatal("crash helper failed before commit", e)
	}
	s, e := NewStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Verify(t.Context()); e != nil {
		t.Fatal(e)
	}
	page, e := s.SearchMemory(t.Context(), MemoryQuery{Scopes: []string{ScopeID("wechat", "synthetic", "crash")}, Originals: true})
	if e != nil || len(page.Items) != 1 || page.Items[0].Event.Trace != "committed" {
		t.Fatal("committed original lost after abrupt exit", page, e)
	}
}

func TestMemorySQLiteFullRollsBackOriginalAndScope(t *testing.T) {
	s, c, in := memoryFixture(t)
	in.Text = "已保留的原话"
	if e := s.Observe(in, c, time.Now()); e != nil {
		t.Fatal(e)
	}
	var pages int
	if e := s.db.QueryRow("PRAGMA page_count").Scan(&pages); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.Exec("PRAGMA max_page_count=" + fmt.Sprint(pages)); e != nil {
		t.Fatal(e)
	}
	in.TraceID = "out-of-space"
	in.Text = strings.Repeat("长原文数据", 20000)
	if e := s.Observe(in, c, time.Now()); e == nil {
		t.Fatal("SQLITE_FULL not propagated")
	}
	id := ScopeID(in.Channel, in.Account, in.Thread)
	scope := s.Context(in, c, time.Now())
	if len(scope.Messages) != 1 {
		t.Fatal("failed disk write changed scope")
	}
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM events WHERE scope=?", id).Scan(&count)
	if count != 1 {
		t.Fatal("failed disk write published event")
	}
	if e := s.Verify(t.Context()); e != nil {
		t.Fatal(e)
	}
}
