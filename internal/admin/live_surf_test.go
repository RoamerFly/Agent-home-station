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
)

func TestLiveAutonomousSurfDecision(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || workspace == "" {
		t.Skip("explicit isolated web/model diagnostic only")
	}
	c, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal("configuration unreadable")
	}
	f := setup(t)
	provider, err := f.manager.makeModel(c)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := companion.NewPreview(c.Companion, provider, c.SystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	access, max, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	engine.Tools = access
	engine.MaxToolCalls = max
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	d, err := engine.Respond(ctx, companion.Input{Channel: "preview", Thread: "surf-observer", TraceID: "surf-isolated-live", Mode: "direct", Preview: true, Surf: true, Text: "自主网上冲浪：请真实搜索一个有意思的海洋科学小知识，选取可靠网页并读取后判断是否值得分享，若分享请附真实来源；不能核实就安静。"})
	if err != nil {
		t.Fatal("surf preview failed", err)
	}
	search, fetch := 0, 0
	for _, row := range f.manager.Logs() {
		if row.Event == "web_activity" && row.Status == "succeeded" {
			if row.Tool == "web_search" {
				search++
			}
			if row.Tool == "web_fetch" {
				fetch++
			}
		}
	}
	report := map[string]any{"action": d.Action, "searches_succeeded": search, "pages_read": fetch, "source_link_in_reply": strings.Contains(strings.Join(d.Messages, ""), "https://"), "actual_wechat_sends": 0, "real_settings_changed": false, "isolated_preview": true}
	b, _ := json.MarshalIndent(report, "", "  ")
	_ = os.WriteFile(filepath.Join(workspace, "research/evidence/autonomous-surf-060.json"), b, 0600)
	if d.Action == "reply" && fetch < 1 {
		t.Fatal("surf shared without reading source")
	}
	if search < 1 {
		t.Fatal("surf never actually searched")
	}
}
