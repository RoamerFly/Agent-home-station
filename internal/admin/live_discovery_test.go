package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

func TestLiveDiscoveryModelPreparesMedia(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || workspace == "" || os.Getenv("WECHAT_DISCOVERY_LIVE") != "1" {
		t.Skip("explicit isolated real discovery/model check")
	}
	c, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	reports := []map[string]any{}
	for _, kind := range []string{"image", "video", "voice"} {
		f := setup(t)
		native := &fakeTools{fakeNative: f.native}
		f.manager.native = native
		f.manager.config = c
		f.manager.config.Companion.QuietStart = "00:00"
		f.manager.config.Companion.QuietEnd = "00:00"
		f.manager.surf = surfDefaults()
		provider, err := f.manager.makeModel(c)
		if err != nil {
			t.Fatal(err)
		}
		engine, err := companion.NewPreview(f.manager.config.Companion, provider, c.SystemPrompt)
		if err != nil {
			t.Fatal(err)
		}
		access, limit, err := f.manager.toolAccess()
		if err != nil {
			t.Fatal(err)
		}
		observed := &reuseObservedTools{inner: access}
		engine.Tools = observed
		engine.MaxToolCalls = limit
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		d, err := engine.Respond(ctx, companion.Input{Channel: "isolated", Account: "test", Thread: "discovery-" + kind, TraceID: "surf-isolated-" + kind, Surf: true, SurfPreferredKind: kind, Mode: "direct", Preview: true, Text: (bot.SurfTask{Topic: "轻松日常、有趣动物、音乐和好看的风景", PreferredKind: kind}).Prompt()})
		cancel()
		media := []companion.GeneratedMedia{}
		for _, id := range d.MediaIDs {
			if item, ok := engine.Media(id); ok {
				media = append(media, item)
			}
		}
		activities := []map[string]any{}
		for _, x := range f.manager.Logs() {
			if x.Event == "web_activity" && x.Status != "running" {
				activities = append(activities, map[string]any{"tool": x.Tool, "state": x.Status, "requests": x.ToolRequests, "bytes": x.Bytes, "reason": x.Reason})
			}
		}
		reports = append(reports, map[string]any{"preferred": kind, "decision": d, "media": media, "tool_calls": observed.calls, "activities": activities, "error": err != nil, "wechat_sends": native.submits.Load(), "sessions": f.manager.surfHistory.snapshot().Sessions})
		if err != nil {
			t.Error(err)
		}
		if native.submits.Load() != 0 {
			t.Fatal("isolated test sent WeChat")
		}
		if len(observed.calls) == 0 || observed.calls[0] != "web_discover" {
			t.Error("search-first loop retained", observed.calls)
		}
		if d.Action != "silent" && d.Action != "reply" {
			t.Error("invalid outcome", d)
		}
	}
	b, _ := json.MarshalIndent(reports, "", "  ")
	if err = os.WriteFile(filepath.Join(workspace, "research/evidence/discovery-model-live.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}
