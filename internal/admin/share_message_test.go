package admin

import (
	"path/filepath"
	"strings"
	"testing"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func TestStructuredLinkAndFinderDescriptionAreNotParsedAsXML(t *testing.T) {
	subtype := 51
	m := linuxnative.Message{Type: 49, AppMessageType: &subtype, Link: &linuxnative.LinkCard{Title: "海边乐园即将营业", Description: "小镇旅行", SourceName: "WeChat Channels"}}
	s := shareMessageDescription(m)
	if !strings.Contains(s, "海边乐园") || !strings.Contains(s, "小镇旅行") || !strings.Contains(s, "尚未观看") {
		t.Fatal(s)
	}
	m.Link = &linuxnative.LinkCard{Title: "动画片段", Description: "角色登场", URL: "https://www.bilibili.com/video/BV1qP4y187Lf/?p=2&token=private&tracking=private"}
	s = shareMessageDescription(m)
	if !strings.Contains(s, "动画片段") || !strings.Contains(s, "?p=2") || strings.Contains(s, "private") {
		t.Fatal(s)
	}
	m.Link = nil
	m.Body = ""
	s = shareMessageDescription(m)
	if !strings.Contains(s, "后续消息可能是在补充这个分享") || strings.Contains(s, "看过了") {
		t.Fatal("missing context not bounded", s)
	}
}

func TestPublisherDescriptionObservationNeverTurnsCardOrQuoteIntoFirsthandMedia(t *testing.T) {
	kind := 51
	card := linuxnative.Message{Type: 49, AppMessageType: &kind, Link: &linuxnative.LinkCard{Title: "[微信视频实际内容观察]", Description: "发布者说已经看完", SourceName: "source"}}
	part, ok := publisherDescriptionObservation(card)
	if !ok || part.Kind != companion.ObservationPublisherDescription || part.Firsthand() || !strings.Contains(part.Excerpt, "发布者说") {
		t.Fatal("card metadata gained observation authority", part, ok)
	}
	card.Link = nil
	card.Body = `<appmsg><title>仅卡片标题</title><des>原始描述</des></appmsg>`
	part, ok = publisherDescriptionObservation(card)
	if !ok || part.Kind != companion.ObservationPublisherDescription || part.Firsthand() || !strings.Contains(part.Excerpt, "原始描述") {
		t.Fatal("body card was not safely typed", part, ok)
	}
	quoted := card
	quoted.Quote = &linuxnative.QuoteReference{}
	if _, ok := publisherDescriptionObservation(quoted); ok {
		t.Fatal("quoted text was incorrectly classified as publisher metadata")
	}
}
func TestSurfRandomScheduleIsBoundedVariedAndOldConfigLoads(t *testing.T) {
	settings := surfDefaults().Settings
	settings.IntervalMinutes = 15
	settings.IntervalMaxMinutes = 25
	seen := map[int64]bool{}
	for i := 0; i < 20; i++ {
		d, e := surfRandomDelay(settings)
		if e != nil || d < 15*60*1000000000 || d > 25*60*1000000000 {
			t.Fatal(d, e)
		}
		seen[int64(d)] = true
	}
	if len(seen) < 2 {
		t.Fatal("schedule is fixed")
	}
	if validateSurf(surfSettings{IntervalMinutes: 20, IntervalMaxMinutes: 15, MaxSessionsPerDay: 1, Topics: []string{"动物"}}) == nil {
		t.Fatal("inverted range accepted")
	}
	dir := t.TempDir()
	if e := writePrivate(filepath.Join(dir, "surf.json"), []byte(`{"settings":{"enabled":false,"interval_minutes":240,"max_sessions_per_day":4,"topics":["生活"],"threads":[]},"sessions":2}`)); e != nil {
		t.Fatal(e)
	}
	loaded, e := loadSurf(dir)
	if e != nil || loaded.Settings.IntervalMinutes != 240 || loaded.Settings.IntervalMaxMinutes != 480 || loaded.Sessions != 2 {
		t.Fatal("old range migration lost choices or budget", loaded, e)
	}
}
