package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestLiveStructuredContextCompressionWithoutWechatSend(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || os.Getenv("WECHAT_CONTEXT_SCHEMA_LIVE") != "1" {
		t.Skip("explicit synthetic memory model validation, no WeChat messages")
	}
	c, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal("private config unavailable")
	}
	f := setup(t)
	provider, err := f.manager.makeModel(c)
	if err != nil {
		t.Fatal("model unavailable")
	}
	cfg := c.Companion
	cfg.Enabled = true
	cfg.Permissions.Remember = true
	cfg.Context = &companion.ContextSettings{AutoCompress: true, CompressAfter: 20, RecentMessages: 5, MaxInputChars: 8000}
	s, _ := companion.NewStore(t.TempDir())
	in := companion.Input{Channel: "wechat", Account: "synthetic", Thread: "summary-test", Sender: "owner"}
	for i := 0; i < 25; i++ {
		in.TraceID = fmt.Sprint(i)
		in.Text = fmt.Sprintf("记录%d：早先聊过火车站，现在话题已结束。最近在聊歌曲《晚风归家》，后续要原始音频，不是火车站视频。", i)
		if err = s.Observe(in, cfg, time.Now().Add(time.Duration(i-25)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	e, _ := companion.New(cfg, s, provider, c.SystemPrompt)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	changed, err := e.CompressContext(ctx, in, true)
	if err != nil || !changed {
		t.Fatal("real model summary failed schema validation", err)
	}
	scope, _ := s.Read(companion.ScopeID(in.Channel, in.Account, in.Thread), cfg, time.Now())
	var summary map[string]any
	if json.Unmarshal([]byte(scope.Summary), &summary) != nil || summary["version"] != float64(1) || scope.CompressedCount != 20 {
		t.Fatal("summary not persisted")
	}
	b, _ := json.MarshalIndent(map[string]any{"validated_schema_version": 1, "compressed_messages": scope.CompressedCount, "raw_messages_preserved": len(scope.Messages), "wechat_sends": 0}, "", "  ")
	if err = os.WriteFile(filepath.Join(workspace, "research/evidence/context-summary-schema-live.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}
