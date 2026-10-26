package bot

import (
	"context"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type stickerAnalyzer struct{ calls int }

func (a *stickerAnalyzer) Analyze(_ context.Context, m linuxnative.Message) (string, error) {
	a.calls++
	if m.Type != 47 {
		panic("wrong media")
	}
	return "[收到表情包的视觉观察] 一只卡通猫挥手，写着你好", nil
}

func TestStickerUsesImagePermissionAndIsRememberedWhenModelChoosesSilence(t *testing.T) {
	w, c, n, _ := planWorker(t, &planReviewModel{answer: `{"action":"silent"}`})
	a := &stickerAnalyzer{}
	w.config.Analyzer = a
	w.config.Options.ReadVideos = false
	w.config.GroupRules = map[string]GroupRule{"room@chatroom": {Mode: "all"}}
	m := fixtureMessage("sticker", "room@chatroom", "", "incoming")
	m.Type = 47
	m.SenderID = "friend"
	w.source = &fakeSource{messages: map[string]linuxnative.Message{m.ID: m}}
	w.handle(context.Background(), fixtureEvent("1", m.ID, m.ThreadID, "incoming", "remote"))
	if a.calls != 1 || len(n.submissions) != 0 {
		t.Fatal("sticker ignored or silence overruled", a.calls)
	}
	scope := c.Store.Context(companion.Input{Channel: "wechat", Account: "test", Thread: m.ThreadID}, c.Config, time.Now())
	found := false
	for _, turn := range scope.Messages {
		found = found || turn.TraceID == m.ID && strings.Contains(turn.Text, "卡通猫")
	}
	if !found {
		t.Fatal("observed sticker missing from user memory", scope.Messages)
	}
	if messageSummary(m) != "[收到表情包]" {
		t.Fatal("sticker logged as generic non-text")
	}
}

func TestStickerReadPermissionIsNotReplacedByVideoPermission(t *testing.T) {
	w, _, _, _ := planWorker(t, &planReviewModel{answer: `{"action":"silent"}`})
	a := &stickerAnalyzer{}
	w.config.Analyzer = a
	w.config.Options.ReadImages = false
	w.config.Options.ReadVideos = true
	m := fixtureMessage("sticker", "filehelper", "", "incoming")
	m.Type = 47
	w.source = &fakeSource{messages: map[string]linuxnative.Message{m.ID: m}}
	w.handle(context.Background(), fixtureEvent("1", m.ID, m.ThreadID, "incoming", "remote"))
	if a.calls != 0 {
		t.Fatal("image permission bypassed")
	}
}

func TestStickerBackgroundAnalysisReachesMemoryWithoutSending(t *testing.T) {
	w, c, n := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) {
		return `{"action":"silent"}`, nil
	}))
	w.enableGenerations()
	defer w.closeGenerations()
	a := &stickerAnalyzer{}
	w.config.Analyzer = a
	w.config.Options.ReadVideos = false
	m := fixtureMessage("animated-sticker", "filehelper", "", "incoming")
	m.Type = 47
	w.source = &fakeSource{messages: map[string]linuxnative.Message{m.ID: m}}
	w.handle(context.Background(), fixtureEvent("1", m.ID, m.ThreadID, "incoming", "remote"))
	pumpGeneration(t, w, func() bool { return len(w.generation.jobs) == 0 })
	scope := c.Store.Context(companion.Input{Channel: "wechat", Account: "test", Thread: m.ThreadID}, c.Config, time.Now())
	found := false
	for _, turn := range scope.Messages {
		found = found || turn.TraceID == m.ID && strings.Contains(turn.Text, "卡通猫")
	}
	if !found || a.calls != 1 || len(n.submissions) != 0 {
		t.Fatal("background sticker lost or force replied", found, a.calls)
	}
}
