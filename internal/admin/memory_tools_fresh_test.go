package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/companion"
)

func TestFreshRoundMemoryToolsHideUnsourcedManualNote(t *testing.T) {
	f, p := trustedAgent(t)
	f.manager.mu.Lock()
	f.manager.config.Companion.Permissions.Remember = true
	cfg := f.manager.config.Companion
	f.manager.mu.Unlock()
	original := companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "native-original", Text: "今天画一张图", Mode: "direct"}
	if err := f.manager.memory.Observe(original, cfg, time.Now()); err != nil {
		t.Fatal(err)
	}
	scope := companion.ScopeID(original.Channel, original.Account, original.Thread)
	observed, err := f.manager.memory.Read(scope, cfg, time.Now())
	if err != nil || len(observed.Messages) == 0 || observed.Messages[0].EventID == "" {
		t.Fatalf("original source missing: %v", err)
	}
	source := &companion.SourceRef{EventID: observed.Messages[0].EventID, MessageID: original.TraceID}
	oldReply := original
	oldReply.TraceID = "task-prior-attempt"
	if err := f.manager.memory.AcceptedDelivery(oldReply, "旧回合的答复", "old-receipt", cfg, time.Now()); err != nil {
		t.Fatal(err)
	}
	withReply, err := f.manager.memory.Read(scope, cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var oldReplyID string
	for _, turn := range withReply.Messages {
		if turn.Role == "assistant" && turn.RequestTraceID == oldReply.TraceID {
			oldReplyID = turn.EventID
		}
	}
	if oldReplyID == "" {
		t.Fatal("old assistant event missing")
	}
	note, err := f.manager.memory.SaveMemory(t.Context(), companion.MemoryEntry{Scope: scope, Subject: original.Sender, Kind: "episode", Detail: "仅旧轮知道的私有笔记", Sources: []companion.MemorySource{}, Manual: true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	query := json.RawMessage(`{"limit":10}`)
	read := json.RawMessage(`{"id":"` + note.ID + `"}`)
	p.SetTrace(original)
	normal, err := p.callMemory(t.Context(), "memory_search", query)
	if err != nil || !strings.Contains(normal, note.Detail) {
		t.Fatalf("normal note missing: %v %s", err, normal)
	}
	if body, err := p.callMemory(t.Context(), "memory_read", read); err != nil || !strings.Contains(body, note.Detail) {
		t.Fatalf("normal note read failed: %v %s", err, body)
	}
	fresh := original
	fresh.TraceID = "task-fresh-attempt"
	fresh.TaskID = "fresh-attempt"
	fresh.OriginTraceID = original.TraceID
	fresh.Source = source
	fresh.FreshRoundRetry = true
	fresh.FreshRoundExcludedEventIDs = []string{oldReplyID}
	p.SetTrace(fresh)
	hidden, err := p.callMemory(t.Context(), "memory_search", query)
	if err != nil || strings.Contains(hidden, note.Detail) {
		t.Fatalf("fresh search exposed unsourced note: %v %s", err, hidden)
	}
	if body, err := p.callMemory(t.Context(), "memory_read", read); err == nil || strings.Contains(body, note.Detail) {
		t.Fatalf("fresh read exposed unsourced note: %v %s", err, body)
	}
	if body, err := p.callMemory(t.Context(), "memory_read", json.RawMessage(`{"id":"`+oldReplyID+`","event":true}`)); err == nil || strings.Contains(body, "旧回合的答复") {
		t.Fatalf("fresh read exposed old assistant event: %v %s", err, body)
	}
	if _, err := f.manager.memory.ReadMemory(t.Context(), []string{scope}, note.ID); err != nil {
		t.Fatalf("fresh view changed durable note: %v", err)
	}
}
