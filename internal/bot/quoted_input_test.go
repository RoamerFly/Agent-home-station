package bot

import (
	"context"
	"strings"
	"testing"
	"time"
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
