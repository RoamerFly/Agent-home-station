package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestLiveResourceDecisionShape(t *testing.T) {
	root := os.Getenv("WECHAT_LIVE_MEDIA_ROOT")
	if root == "" {
		t.Skip("explicit isolated diagnostic")
	}
	c, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	f := setup(t)
	f.manager.native = &fakeTools{fakeNative: f.native}
	provider, err := f.manager.makeModel(c)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := previewModel{chat: func(ctx context.Context, system string, turns []map[string]string) (string, error) {
		a, e := provider.Chat(ctx, system, turns)
		var value any
		_ = json.Unmarshal([]byte(a), &value)
		if e == nil {
			t.Logf("decision content: %.1600s", a)
		} else {
			t.Logf("model error: %v", e)
		}
		return a, e
	}}
	e, _ := companion.NewPreview(c.Companion, wrapped, c.SystemPrompt)
	e.Tools, e.MaxToolCalls, err = f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	d, err := e.Respond(ctx, companion.Input{Mode: "direct", Preview: true, Text: "给我发一首好听的流行风格音乐，发语音；可以选择一首公开可下载的器乐，明确曲名和来源。"})
	t.Logf("decision action=%s reason=%s messages=%d media=%d err=%v", d.Action, d.Reason, len(d.Messages), len(d.MediaIDs), err)
}
