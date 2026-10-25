package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/mcptools"
)

type contextModel struct{ seen string }

func (m *contextModel) Chat(_ context.Context, _ string, turns []map[string]string) (string, error) {
	m.seen = turns[0]["content"]
	return `{"version":1,"people":[],"topics":[],"current_focus":"最近歌曲","media":[{"url":"https://www.bilibili.com/video/BV1S1oeYbEe1/"}],"pending_tasks":[],"preferences":[],"corrections":[]}`, nil
}
func (m *contextModel) ChatFunctions(ctx context.Context, s string, t []map[string]string, _ []mcptools.Tool) (string, error) {
	return m.Chat(ctx, s, t)
}
func TestCompressionKeepsArchiveButModelNeverReloadsCoveredPrefix(t *testing.T) {
	cfg := enabledConfig()
	cfg.MemoryMessages = 500
	cfg.Permissions.Remember = true
	cfg.Context = &ContextSettings{true, 20, 5, 8000}
	store, _ := NewStore(t.TempDir())
	in := Input{Channel: "wechat", Account: "a", Thread: "thread", Mode: "direct", Sender: "owner"}
	now := time.Now()
	for i := 0; i < 25; i++ {
		in.TraceID = fmt.Sprint(i)
		in.Text = fmt.Sprintf("message-%02d", i)
		store.Observe(in, cfg, now.Add(time.Duration(i)*time.Millisecond))
	}
	model := &contextModel{}
	e, _ := New(cfg, store, model, "policy")
	ok, err := e.CompressContext(context.Background(), in, true)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	raw, _ := store.Read(ScopeID("wechat", "a", "thread"), cfg, time.Now())
	if raw.CompressedCount != 20 || len(raw.Messages) != 25 || raw.Summary == "" {
		t.Fatal(raw)
	}
	scope := store.Context(in, cfg, time.Now())
	if len(scope.Messages) != 5 || scope.Messages[0].Text != "message-20" {
		t.Fatal("covered messages leaked", scope)
	}
	turns := e.contextTurns(scope, Input{TraceID: "new"})
	if strings.Contains(turns[0]["content"], "message-00") || !strings.Contains(turns[0]["content"], "message-20") {
		t.Fatal("old raw reread", turns)
	}
	in.TraceID = "new"
	in.Text = "message-new"
	store.Observe(in, cfg, time.Now())
	_, err = e.CompressContext(context.Background(), in, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(model.seen, "message-00") {
		t.Fatal("compressor reread compressed prefix")
	}
}
func TestMediaFocusBackfillIsScopedAndChronologyPreserved(t *testing.T) {
	cfg := enabledConfig()
	cfg.Permissions.Remember = true
	s, _ := NewStore(t.TempDir())
	in := Input{Channel: "wechat", Account: "a", Thread: "one", TraceID: "old", Text: "old"}
	s.Observe(in, cfg, time.Now())
	s.ObserveMedia(in, cfg, MediaMemory{Key: "media", AssetID: "asset-a", Kind: "voice", Title: "最近弹唱", State: "accepted"}, time.Now())
	id := ScopeID("wechat", "a", "one")
	if err := s.BackfillMediaSources(map[string]map[string]string{id: {"asset-a": "https://www.bilibili.com/video/BV1S1oeYbEe1/"}}); err != nil {
		t.Fatal(err)
	}
	scope := s.Context(in, cfg, time.Now())
	media := recentMediaFocus(scope)
	if len(media) != 1 || media[0].URL == "" {
		t.Fatal(media)
	}
	other := in
	other.Thread = "two"
	if len(recentMediaFocus(s.Context(other, cfg, time.Now()))) != 0 {
		t.Fatal("cross thread source")
	}
	e, _ := New(cfg, s, &contextModel{}, "p")
	turns := e.contextTurns(scope, Input{TraceID: "current"})
	var payload map[string]any
	if json.Unmarshal([]byte(turns[0]["content"]), &payload) != nil || payload["ordering"] != "oldest_to_newest" {
		t.Fatal(turns)
	}
}
