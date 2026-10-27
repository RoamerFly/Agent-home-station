package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
)

// Opt-in: real provider, synthetic conversations, isolated tasks and fake
// WeChat transport. Never opens the live message store or sends a real message.
func TestLiveFriendlyPersonaAndOrdinaryMemberReminder(t *testing.T) {
	if os.Getenv("WECHAT_FRIENDLY_LIVE") != "1" {
		t.Skip("explicit provider-only live verification")
	}
	root := os.Getenv("WECHAT_LIVE_MEDIA_ROOT")
	live, e := loadConfig(filepath.Join(root, "admin"))
	if e != nil {
		t.Fatal("provider config unavailable")
	}
	f, p, n := memberReminder(t)
	key := filepath.Join(f.dir, "model-key")
	if e = os.WriteFile(key, []byte(live.APIKey), 0600); e != nil {
		t.Fatal(e)
	}
	model, e := deepseek.New(deepseek.Config{BaseURL: live.BaseURL, Model: live.Model, KeyFile: key, Compatible: live.Provider == "openai-compatible", MaxTokens: 4096})
	if e != nil {
		t.Fatal(e)
	}
	c := companion.Defaults()
	c.Enabled = true
	c.Permissions.Remember = false
	c.QuietStart = "00:00"
	c.QuietEnd = "00:00"
	reports := []map[string]any{}
	for i, text := range []string{"今天上班的精神状态：我的灵魂已经下班，肉体还在工位。", "今天面试没过，觉得自己特别没用，不想听大道理。", "最近总觉得自己走得太慢，你能不能讲个小故事陪我缓缓？", "两小时后提醒我喝水。"} {
		engine, e := companion.New(c, f.manager.memory, model, live.SystemPrompt)
		if e != nil {
			t.Fatal(e)
		}
		engine.Tools = p
		engine.MaxToolCalls = 8
		in := companion.Input{Channel: "wechat", Account: "test", Thread: "room@chatroom", Sender: "wxid_member", TraceID: time.Now().Format("150405.000000000"), Mode: "direct", Directed: true, IsGroup: true, Text: text}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		d, e := engine.Respond(ctx, in)
		cancel()
		if e != nil || d.Action != "reply" || len(d.Messages) == 0 {
			t.Fatalf("scenario %d failed: %v action=%s", i, e, d.Action)
		}
		reports = append(reports, map[string]any{"scenario": text, "messages": d.Messages, "action": d.Action})
	}
	tasks := f.manager.agent.list("test", "room@chatroom")
	if len(tasks) != 1 || tasks[0].Reminder == nil || tasks[0].OwnerRequested || time.Until(tasks[0].Next) < 119*time.Minute {
		t.Fatal("model did not create correct persistent ordinary-member reminder")
	}
	if len(n.requests) != 0 {
		t.Fatal("provider-only test sent a message")
	}
	if target := os.Getenv("WECHAT_FRIENDLY_REPORT"); target != "" {
		b, _ := json.MarshalIndent(map[string]any{"real_model": true, "wechat_sends": 0, "persistent_member_reminder": true, "scenarios": reports}, "", "  ")
		if e = os.WriteFile(target, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	t.Log("Real model replied in four synthetic scenarios and created a 7200-second member reminder; no real WeChat messages")
}
