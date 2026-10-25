package bot

import (
	"context"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type quotedTestSource struct {
	*fakeSource
	resolution linuxnative.QuoteResolution
}

func (s *quotedTestSource) QuotedMessage(context.Context, string) (linuxnative.QuoteResolution, error) {
	return s.resolution, nil
}

func TestQuotedReplyKeepsUserTextAndVerifiedBotReferenceWithoutFakeMention(t *testing.T) {
	w, _, _, _ := mediaWorker(t)
	type57 := 57
	no := false
	m := fixtureMessage("incoming-quote", "room@chatroom", "还是文件吧", "incoming")
	m.Type = 49
	m.AppMessageType = &type57
	m.Quote = &linuxnative.QuoteReference{ServerID: "777", Type: 1}
	m.Mentions.State = "known"
	m.Mentions.Self = &no
	original := fixtureMessage("bot-original", "room@chatroom", "这首音乐可以发原文件", "outgoing")
	original.ServerID = "777"
	w.source = &quotedTestSource{fakeSource: &fakeSource{}, resolution: linuxnative.QuoteResolution{AccountID: "test", MessageID: m.ID, ThreadID: m.ThreadID, State: "resolved", Message: &original}}
	quoteText := w.readQuoteContext(context.Background(), &m)
	if !m.ReplyToSelf || *m.Mentions.Self || !strings.Contains(quoteText, "这首音乐可以发原文件") || !strings.Contains(quoteText, `"from_bot_account":true`) {
		t.Fatal(m, quoteText)
	}
	w.config.GroupRules = map[string]GroupRule{"room@chatroom": {Mode: "mention"}}
	working := quotedPlainBody(m)
	prompt, eligible := w.prompt(working, time.Now())
	if !eligible || prompt != "还是文件吧" || !w.addressed(m) || strings.Contains(messageSummary(m), "分享卡片") {
		t.Fatal("quoted text misclassified", prompt, eligible, messageSummary(m))
	}
	bad := w.source.(*quotedTestSource)
	bad.resolution.ThreadID = "other@chatroom"
	m.ReplyToSelf = false
	quoteText = w.readQuoteContext(context.Background(), &m)
	if m.ReplyToSelf || strings.Contains(quoteText, "这首音乐可以发原文件") {
		t.Fatal("cross-chat quoted content admitted")
	}
}

func TestQuotedShareRetainsExactSourceWithoutNeedingCachedAsset(t *testing.T) {
	w, _, _, _ := mediaWorker(t)
	m := fixtureMessage("quote-card", "room@chatroom", "把这个原视频发我", "incoming")
	m.Quote = &linuxnative.QuoteReference{ServerID: "777", Type: 49}
	original := fixtureMessage("original-card", m.ThreadID, "", "outgoing")
	original.Type, original.ServerID = 49, "777"
	original.Link = &linuxnative.LinkCard{Title: "猫咪看雪", Description: "公开播放卡片", URL: "https://www.bilibili.com/video/BV1425hzpEtn/?p=2"}
	w.source = &quotedTestSource{fakeSource: &fakeSource{}, resolution: linuxnative.QuoteResolution{AccountID: "test", MessageID: m.ID, ThreadID: m.ThreadID, State: "resolved", Message: &original}}
	w.runtime.Media = []*mediaDelivery{{Input: companion.Input{Thread: m.ThreadID, Sender: "alice"}, Kind: "share", Title: original.Link.Title, URL: original.Link.URL, Status: linuxnative.ToolStatus{ServerID: "777"}}}
	raw := w.readQuoteContext(context.Background(), &m)
	if !m.ReplyToSelf || !strings.Contains(raw, `"source_url":"https://www.bilibili.com/video/BV1425hzpEtn/?p=2"`) || !strings.Contains(raw, `"kind":"share"`) || !strings.Contains(raw, `"requester_id":"alice"`) {
		t.Fatal(raw)
	}
	// After runtime history retention, the verified native card still supplies
	// its exact source; a same-title recent card must not replace this one.
	w.runtime.Media = nil
	raw = w.readQuoteContext(context.Background(), &m)
	if !strings.Contains(raw, `"source_url":"https://www.bilibili.com/video/BV1425hzpEtn/?p=2"`) {
		t.Fatal(raw)
	}
	for _, bad := range []string{"javascript:alert(1)", "https://user:password@example.org/video", "file:///private/movie"} {
		if quotedSourceURL(bad) != "" {
			t.Fatal("unsafe quoted source", bad)
		}
	}
}

func TestMediaProvenanceSurvivesRuntimeRecovery(t *testing.T) {
	w, _, c, in := mediaWorker(t)
	in.Sender, in.SenderName, in.OriginTraceID, in.TaskID = "alice", "小甲", "original-request", "task-1"
	m := companion.GeneratedMedia{ID: "source", Kind: "voice", AssetID: "audio", ParentAssetID: "video", CachedMIME: "audio/mp4", URL: "https://example.org/video", Title: "音轨"}
	if !w.queueMedia(context.Background(), c, in, m) {
		t.Fatal("not queued")
	}
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	d := w.runtime.Media[0]
	if d.ParentAssetID != "video" || d.CachedMIME != "audio/mp4" || d.Input.OriginTraceID != "original-request" {
		t.Fatal(d)
	}
	d.State = "accepted"
	w.mediaRecord(d, "media_sent", "sent")
	scope := c.Store.Context(in, c.Config, time.Now())
	if len(scope.Messages) == 0 {
		t.Fatal("no memory")
	}
	got := scope.Messages[len(scope.Messages)-1].Media
	if got == nil || got.ParentAssetID != "video" || got.CachedMIME != "audio/mp4" || got.RequestTraceID != "original-request" || got.RequesterID != "alice" {
		t.Fatal(got)
	}
}
