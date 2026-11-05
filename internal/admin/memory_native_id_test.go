package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestMemoryReadResolvesNativeIDWithoutGuessingLegacyIdentity(t *testing.T) {
	f, p := globalSearchFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "source@chatroom", Sender: "old-member", TraceID: "msg_synthetic_native", Text: "旧数据集原文", MessageTime: now.Add(-time.Hour).Format(time.RFC3339)}
	c := f.manager.Config().Companion
	if err := f.manager.memory.Observe(in, c, now); err != nil {
		t.Fatal(err)
	}
	in.Sender = "current-member"
	in.Text = "当前数据集原文"
	in.MessageTime = now.Format(time.RFC3339)
	in.Source = &companion.SourceRef{InstanceID: f.manager.memory.SourceInstanceID(), MessageID: in.TraceID, ServerID: "current-server"}
	if err := f.manager.memory.Observe(in, c, now); err != nil {
		t.Fatal(err)
	}
	for _, selector := range []string{"id", "message_id"} {
		args, _ := json.Marshal(map[string]any{selector: in.TraceID, "thread_id": in.Thread})
		raw, err := p.Call(t.Context(), "memory_read", args)
		if err != nil || !strings.Contains(raw, "当前数据集原文") || strings.Contains(raw, "旧数据集原文") {
			t.Fatal("native ID resolved wrong dataset", err, raw)
		}
	}
	if err := f.manager.memory.Clear(companion.ScopeID(in.Channel, in.Account, in.Thread)); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"message_id": in.TraceID, "thread_id": in.Thread})
	if _, err := p.Call(t.Context(), "memory_read", args); err == nil {
		t.Fatal("native alias bypassed forgetting")
	}
}
