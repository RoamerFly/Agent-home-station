package admin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

// Synthetic conversation, actual configured language model, no tools or native
// sender. This validates planning, not real-world social quality or delivery.
func TestLiveAutonomousPlanningWithoutSending(t *testing.T) {
	root := os.Getenv("WECHAT_AUTONOMY_LIVE_ROOT")
	if root == "" {
		t.Skip("explicit synthetic autonomy validation only")
	}
	cfg, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal("configuration unavailable")
	}
	f := setup(t)
	f.manager.config = cfg
	model, err := f.manager.makeModel(cfg)
	if err != nil {
		t.Fatal("configured model unavailable")
	}
	c := cfg.Companion
	c.Enabled = true
	c.Permissions.Proactive = true
	c.Permissions.Remember = true
	c.Permissions.ReplyText = true
	c.Conversation = companion.ConversationDefaults()
	c.Conversation.AllowWake = true
	c.QuietStart = "00:00"
	c.QuietEnd = "00:00"
	c.ProactiveThreads = []string{"synthetic-group@chatroom"}
	c.ProactiveRules = nil
	engine, err := companion.NewPreview(c, model, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	d, err := engine.Respond(ctx, companion.Input{Channel: "preview", Account: "synthetic", Thread: c.ProactiveThreads[0], Mode: "proactive", IsGroup: true, Preview: true, Text: "这是合成测试情境：大家刚结束聊天，约好两小时后讨论一首新歌。当前没有新内容，不要发消息；请选择安静，并安排7200秒后重新评估，用wake_reason记下要跟进的事。默认建议时间不是上限。"})
	if err != nil {
		t.Fatal("configured model request failed")
	}
	if d.Action != "silent" || d.WakeAfterSeconds != 7200 || d.WakeReason == "" {
		t.Fatalf("planning expectation not met: action=%s delay=%d has_purpose=%t", d.Action, d.WakeAfterSeconds, d.WakeReason != "")
	}
	t.Log("actual_model=true action=silent explicit_wake_seconds=7200 purpose_persistable=true live_memory_writes=0 wechat_sends=0")
}
