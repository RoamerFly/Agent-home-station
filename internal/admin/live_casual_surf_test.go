package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

func TestLiveCasualSurfPreparesVideoCardAndMusic(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || workspace == "" || os.Getenv("WECHAT_CASUAL_SURF") != "1" {
		t.Skip("explicit isolated surf/model check")
	}
	c, e := loadConfig(filepath.Join(root, "admin"))
	if e != nil {
		t.Fatal(e)
	}
	reports := []map[string]any{}
	for _, tc := range []struct{ kind, topic string }{{"video", "可爱小猫的日常视频"}, {"voice", "轻松好听的公开音乐"}} {
		f := setup(t)
		native := &fakeTools{fakeNative: f.native}
		f.manager.native = native
		f.manager.config.Companion = c.Companion
		provider, e := f.manager.makeModel(c)
		if e != nil {
			t.Fatal(e)
		}
		engine, e := companion.NewPreview(c.Companion, provider, c.SystemPrompt)
		if e != nil {
			t.Fatal(e)
		}
		access, limit, e := f.manager.toolAccess()
		if e != nil {
			t.Fatal(e)
		}
		observed := &reuseObservedTools{inner: access}
		engine.Tools = observed
		engine.MaxToolCalls = limit
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		d, err := engine.Respond(ctx, companion.Input{Channel: "isolated", Account: "test", Thread: "surf-" + tc.kind, Mode: "direct", Surf: true, Preview: true, Text: (bot.SurfTask{Topic: tc.topic, PreferredKind: tc.kind}).Prompt()})
		cancel()
		media := []companion.GeneratedMedia{}
		for _, id := range d.MediaIDs {
			if item, ok := engine.Media(id); ok {
				media = append(media, item)
			}
		}
		activity := []map[string]any{}
		for _, x := range f.manager.Logs() {
			if x.Event == "web_activity" && x.Status != "running" {
				activity = append(activity, map[string]any{"tool": x.Tool, "status": x.Status, "bytes": x.Bytes, "source": x.TargetURL})
			}
		}
		reports = append(reports, map[string]any{"preferred_kind": tc.kind, "decision": d, "selected_media": media, "tool_calls": observed.calls, "activities": activity, "error": err != nil, "wechat_sends": native.submits.Load()})
		text := strings.Join(d.Messages, " ")
		if err != nil || strings.Contains(text, "刚冲浪看到") || strings.Contains(text, "量子期刊") || strings.Contains(text, "论文") {
			t.Error("casual style not applied", tc.kind, d, err)
		}
		if len(media) == 0 {
			t.Error("no real media prepared", tc.kind, d, observed.calls)
		}
		if native.submits.Load() != 0 {
			t.Fatal("isolated surf sent WeChat")
		}
	}
	b, _ := json.MarshalIndent(reports, "", "  ")
	if e = os.WriteFile(filepath.Join(workspace, "research/evidence/casual-surf-live.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
}
