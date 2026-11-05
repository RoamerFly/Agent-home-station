package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func memoryFixture(t *testing.T) (*Store, Config, Input) {
	t.Helper()
	s, e := NewStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	c := Defaults()
	// Source/review fixtures explicitly exercise the optional model check.
	c.Grounding = &GroundingSettings{Enabled: true}
	c.Permissions.Remember = true
	return s, c, Input{Channel: "wechat", Account: "synthetic", Thread: "room", Sender: "alice", TraceID: "source"}
}
func TestMemorySourcesCorrectionAndForgetting(t *testing.T) {
	s, c, in := memoryFixture(t)
	ctx := context.Background()
	in.Text = "我喜欢喝红茶，不喝咖啡。"
	if e := s.Observe(in, c, time.Now().Add(-time.Hour)); e != nil {
		t.Fatal(e)
	}
	id := ScopeID(in.Channel, in.Account, in.Thread)
	events, e := s.SearchMemory(ctx, MemoryQuery{Scopes: []string{id}, Query: "红茶", Originals: true})
	if e != nil || len(events.Items) != 1 {
		t.Fatal(events, e)
	}
	source := events.Items[0].Event.ID
	m, e := s.SaveMemory(ctx, MemoryEntry{Scope: id, Subject: "alice", Kind: "preference", Detail: "喜欢红茶，不喝咖啡", Sources: []MemorySource{{source, "我喜欢喝红茶，不喝咖啡。"}}}, 0)
	if e != nil {
		t.Fatal(e)
	}
	bad := m
	bad.ID = ""
	bad.Subject = "bob"
	if _, e = s.SaveMemory(ctx, bad, 0); e == nil {
		t.Fatal("different speaker accepted")
	}
	in.TraceID = "correction"
	in.Text = "更正一下，我现在喜欢喝绿茶。"
	if e = s.Observe(in, c, time.Now()); e != nil {
		t.Fatal(e)
	}
	p, e := s.SearchMemory(ctx, MemoryQuery{Scopes: []string{id}, Query: "绿茶", Originals: true})
	if e != nil || len(p.Items) == 0 {
		t.Fatal(p, e)
	}
	newM, e := s.SaveMemory(ctx, MemoryEntry{Scope: id, Subject: "alice", Kind: "preference", Detail: "现在喜欢绿茶", Supersedes: m.ID, Sources: []MemorySource{{p.Items[0].Event.ID, in.Text}}}, 0)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ReadMemory(ctx, []string{id}, m.ID); e == nil {
		t.Fatal("superseded fact visible")
	}
	if _, e = s.ReadMemory(ctx, []string{ScopeID("wechat", "other", "room")}, newM.ID); e == nil {
		t.Fatal("cross account disclosure")
	}
	if e = s.SaveEmbedding(ctx, newM, "synthetic-space", []float32{1, 0}); e != nil {
		t.Fatal(e)
	}
	p, e = s.SearchMemory(ctx, MemoryQuery{Scopes: []string{id}, Query: "完全换个说法", Space: "synthetic-space", Vector: []float32{1, 0}})
	if e != nil || len(p.Items) != 1 {
		t.Fatal("semantic query", p, e)
	}
	if e = s.Clear(id); e != nil {
		t.Fatal(e)
	}
	for _, originals := range []bool{false, true} {
		p, e = s.SearchMemory(ctx, MemoryQuery{Scopes: []string{id}, Originals: originals, Vector: []float32{1, 0}, Space: "synthetic-space"})
		if e != nil || len(p.Items) > 0 {
			t.Fatal("clear resurrected memory", p, e)
		}
	}
}

func TestMemoryHistoryHidesForgottenEvidenceFromEarlierVersion(t *testing.T) {
	s, cfg, in := memoryFixture(t)
	ctx := t.Context()
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	rememberSource := func(trace, text string) []MemorySource {
		t.Helper()
		in.TraceID, in.Text = trace, text
		if err := s.Observe(in, cfg, time.Now()); err != nil {
			t.Fatal(err)
		}
		page, err := s.SearchMemory(ctx, MemoryQuery{Scopes: []string{scope}, Query: text, Originals: true})
		if err != nil || len(page.Items) == 0 || page.Items[0].Event.Text != text {
			t.Fatal("source fixture not found", err)
		}
		return []MemorySource{{page.Items[0].Event.ID, text}}
	}
	first := rememberSource("original", "想寄一张海边明信片")
	mem, err := s.SaveMemory(ctx, MemoryEntry{Scope: scope, Subject: in.Sender, Kind: "episode", Detail: "先前约定", Sources: first}, 0)
	if err != nil {
		t.Fatal(err)
	}
	mem.Detail = "新的约定"
	mem.Sources = rememberSource("correction", "改为寄出雪山画册")
	mem, err = s.SaveMemory(ctx, mem, mem.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, prior, err := s.MemoryHistory(ctx, []string{scope}, mem.ID); err != nil || len(prior) != 1 || prior[0].Sources[0] != first[0] {
		t.Fatal("visible original version missing", err)
	}
	// Forget the original event through another memory that cites it. Only
	// the corrected version, which has independent evidence, remains readable.
	other, err := s.SaveMemory(ctx, MemoryEntry{Scope: scope, Subject: in.Sender, Kind: "fact", Detail: "引用同一原句", Sources: first}, 0)
	if err != nil {
		t.Fatal(err)
	}
	other.Status = "deleted"
	if _, err := s.SaveMemory(ctx, other, other.Version); err != nil {
		t.Fatal(err)
	}
	current, prior, err := s.MemoryHistory(ctx, []string{scope}, mem.ID)
	if err != nil || current.ID != mem.ID || len(prior) != 0 {
		t.Fatal("forgotten history disclosed or independent correction lost", err)
	}
}

func TestMemoryMigrationCompleteNewOriginalsAndBackup(t *testing.T) {
	dir := t.TempDir()
	id := ScopeID("wechat", "synthetic", "room")
	old := persisted{Version: 1, Scopes: map[string]Scope{id: {ID: id, Channel: "wechat", Account: "synthetic", Thread: "room", Messages: []Turn{{Role: "user", Text: "legacy preview", Sender: "alice", Time: time.Now()}}}}}
	b, _ := json.Marshal(old)
	path := filepath.Join(dir, "memory.json")
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	s, e := NewStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	if e = s.Verify(ctx); e != nil {
		t.Fatal(e)
	}
	c := Defaults()
	c.Permissions.Remember = true
	text := strings.Repeat("长原文", 1200) + "末尾事实"
	if e = s.Observe(Input{Channel: "wechat", Account: "synthetic", Thread: "room", Sender: "alice", TraceID: "long", Text: text}, c, time.Now()); e != nil {
		t.Fatal(e)
	}
	p, e := s.SearchMemory(ctx, MemoryQuery{Scopes: []string{id}, Query: "末尾事实", Originals: true})
	if e != nil || len(p.Items) == 0 {
		t.Fatal(p, e)
	}
	ev, e := s.ReadEvent(ctx, []string{id}, p.Items[0].Event.ID)
	if e != nil || ev.Text != text || !ev.Complete {
		t.Fatal("lost full text", e)
	}
	if e = s.Backup(ctx, filepath.Join(dir, "backup.sqlite")); e != nil {
		t.Fatal(e)
	}
	kept, _ := os.ReadFile(path)
	if string(kept) != string(b) {
		t.Fatal("legacy source overwritten")
	}
	s.Close()
	s, e = NewStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ev, e = s.ReadEvent(ctx, []string{id}, ev.ID)
	if e != nil || ev.Text != text {
		t.Fatal("restart lost original", e)
	}
}

func TestMemoryRecallHundredSourceQueries(t *testing.T) {
	s, c, in := memoryFixture(t)
	ctx := context.Background()
	id := ScopeID(in.Channel, in.Account, in.Thread)
	for i := 0; i < 100; i++ {
		in.TraceID = fmt.Sprint("source-", i)
		in.Text = fmt.Sprintf("地点%03d的约定是明天下午喝茶，编号event%03d。", i, i)
		if e := s.Observe(in, c, time.Now()); e != nil {
			t.Fatal(e)
		}
	}
	hits := 0
	for i := 0; i < 100; i++ {
		p, e := s.SearchMemory(ctx, MemoryQuery{Scopes: []string{id}, Query: fmt.Sprintf("event%03d", i), Originals: true, Limit: 5})
		if e != nil {
			t.Fatal(e)
		}
		for _, h := range p.Items {
			if h.Event.Trace == fmt.Sprint("source-", i) {
				hits++
				break
			}
		}
	}
	if hits < 90 {
		t.Fatalf("recall@5=%d/100", hits)
	}
	t.Logf("synthetic exact-source recall@5=%d/100; semantic paraphrases tested separately", hits)
}
