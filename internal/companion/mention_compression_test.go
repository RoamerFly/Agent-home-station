package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

type chronologicalCompressionProbe struct{ request string }

func (p *chronologicalCompressionProbe) Chat(_ context.Context, _ string, turns []map[string]string) (string, error) {
	p.request = turns[0]["content"]
	return `{"version":1,"people":[],"topics":[],"current_focus":"群聊记录","media":[],"pending_tasks":[],"preferences":[],"corrections":[]}`, nil
}

func TestMentionHistoryCompressionUsesMessageTimeWithoutReorderingStoredBoundary(t *testing.T) {
	cfg := enabledConfig()
	cfg.MemoryMessages = 100
	cfg.Permissions.Remember = true
	cfg.Context = &ContextSettings{AutoCompress: true, CompressAfter: 20, RecentMessages: 5, MaxInputChars: 8000}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	in := Input{Channel: "wechat", Account: "fixture", Thread: "room@chatroom", Sender: "member"}
	base := time.Now().UTC().Add(-time.Hour)
	for i := range 25 {
		in.TraceID, in.Text = fmt.Sprintf("m%02d", i), fmt.Sprintf("群消息-%02d", i)
		at := base.Add(time.Duration(i+1) * time.Second)
		if i == 0 {
			at = base.Add(2 * time.Second)
		} else if i == 1 {
			at = base.Add(time.Second)
		}
		in.MessageTime = at.Format(time.RFC3339Nano)
		if err := store.Observe(in, cfg, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	probe := &chronologicalCompressionProbe{}
	engine, err := New(cfg, store, probe, "policy")
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := engine.CompressContext(t.Context(), in, true); !changed || err != nil {
		t.Fatalf("compression failed: changed=%v err=%v", changed, err)
	}
	var request struct {
		Messages []struct {
			MessageID string `json:"message_id"`
		} `json:"new_chronological_messages"`
	}
	if err := json.Unmarshal([]byte(probe.request), &request); err != nil || len(request.Messages) != 20 {
		t.Fatalf("missing compression batch: err=%v rows=%d", err, len(request.Messages))
	}
	if request.Messages[0].MessageID != "m01" || request.Messages[1].MessageID != "m00" {
		t.Fatalf("compression input is not in original message-time order: %s, %s", request.Messages[0].MessageID, request.Messages[1].MessageID)
	}
	scope, err := store.Read(ScopeID(in.Channel, in.Account, in.Thread), cfg, time.Now())
	if err != nil || scope.CompressedCount != 20 || scope.Messages[0].TraceID != "m00" {
		t.Fatalf("stored arrival order or compression boundary changed: err=%v count=%d", err, scope.CompressedCount)
	}
}
