package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
)

// Opt-in real model planning, synthetic messages/file, fake WeChat transport.
func TestLiveOwnerPlansCrossGroupOriginalFileForward(t *testing.T) {
	root := os.Getenv("WECHAT_LIVE_MEDIA_ROOT")
	if root == "" || os.Getenv("WECHAT_CROSS_FORWARD_LIVE") != "1" {
		t.Skip("explicit real model, synthetic WeChat only")
	}
	live, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	f, p, n := forwardFixture(t)
	c := f.manager.config
	c.GroupRules = []GroupRule{{ID: "source@chatroom", Name: "B群", Mode: "mention"}, {ID: "target@chatroom", Name: "A群", Mode: "mention"}}
	c.Companion.Enabled = true
	c.Companion.Permissions.Remember = true
	c.Companion.Permissions.ReplyText = true
	c.Companion.Permissions.ObserveGroups = true
	c.Companion.QuietStart = "00:00"
	c.Companion.QuietEnd = "00:00"
	f.manager.config = c
	key := filepath.Join(f.dir, "live-key")
	if err = os.WriteFile(key, []byte(live.APIKey), 0600); err != nil {
		t.Fatal(err)
	}
	model, err := deepseek.New(deepseek.Config{BaseURL: live.BaseURL, Model: live.Model, KeyFile: key, Compatible: live.Provider == "openai-compatible", MaxTokens: 4096})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := companion.New(c.Companion, f.manager.memory, model, c.SystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	engine.Tools = p
	engine.MaxToolCalls = 20
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	_, err = engine.Respond(ctx, companion.Input{Channel: "wechat", Account: "test", Thread: "target@chatroom", Sender: "wxid_owner", TraceID: "synthetic-owner-forward", Mode: "direct", Directed: true, IsGroup: true, Text: "把B群刚刚的文件发到这里。要真实的原文件附件，不是文字描述。用现有微信工具自行完成，并核对发送回执。不要操作服务器。"})
	if err != nil {
		t.Fatal(err)
	}
	if n.submits.Load() != 1 || n.downloads != 1 || n.send.ThreadID != "target@chatroom" || string(n.uploaded) != "synthetic attachment bytes" {
		t.Fatal("model did not forward original to A group", n.send, n.submits.Load(), n.downloads)
	}
	verified := false
	for _, op := range f.manager.agent.operations() {
		if op.Tool == "wechat_forward_file" && strings.Contains(op.Result, `"state":"accepted"`) {
			verified = true
		}
	}
	if !verified {
		t.Fatal("model did not inspect actual receipt")
	}
	if report := os.Getenv("WECHAT_CROSS_FORWARD_REPORT"); report != "" {
		b, _ := json.MarshalIndent(map[string]any{"real_model": true, "synthetic_messages_and_file": true, "source_group_resolved": n.catalogThread == "source@chatroom", "original_file_bytes_match": true, "one_fake_send": true, "target_group_correct": true, "receipt_checked": true, "real_wechat_sends": 0}, "", "  ")
		if err = os.WriteFile(report, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
