package adapter

import (
	"strings"
	"testing"

	"wechat-bot/internal/linuxnative"
)

func TestCardDescriptionSeparatesPublisherCopyFromObservedContent(t *testing.T) {
	kind := 51
	message := linuxnative.Message{
		Type:           49,
		AppMessageType: &kind,
		Link: &linuxnative.LinkCard{
			Title:       "亲子活动视频",
			Description: "孩子们在公园放风筝",
			SourceName:  "测试发布者",
			URL:         "https://www.bilibili.com/video/BV1xx?token=secret&p=2#private",
		},
	}
	part, ok := PublisherCardDescription(message)
	if !ok || part.Range != "分享卡片元数据" || !strings.Contains(part.Excerpt, "亲子活动") {
		t.Fatal(part, ok)
	}
	card := CardDescription(message)
	if !strings.Contains(card, "未观看视频") || strings.Contains(card, "secret") || strings.Contains(card, "#private") || !strings.Contains(card, "p=2") {
		t.Fatal(card)
	}
	observed := ObservedCardDescription(message, "[实际内容已读取]")
	if !strings.HasPrefix(observed, "[实际内容已读取]") || strings.Contains(observed, "未观看视频") {
		t.Fatal(observed)
	}
}

func TestCardBodyDescriptionNeverReleasesURLOrInstructions(t *testing.T) {
	body := `<msg><appmsg><title>亲子活动视频</title><des>孩子们在公园放风筝</des><url>https://private.invalid/watch?token=secret</url><finderFeed><desc>户外运动</desc><nickname>作者</nickname></finderFeed></appmsg></msg>`
	text := CardBodyDescription(body)
	if !strings.Contains(text, "亲子活动") || !strings.Contains(text, "未观看") || strings.Contains(text, "secret") || strings.Contains(text, "private.invalid") {
		t.Fatal(text)
	}
	if !strings.Contains(CardBodyDescription("not XML"), "没有可读取") {
		t.Fatal("malformed card treated as video")
	}
}

func TestPublicShareURLKeepsWeChatArticleIdentity(t *testing.T) {
	got := PublicShareURL("https://mp.weixin.qq.com/s?__biz=abc&mid=123&idx=1&sn=xyz&t=private&scene=2#part")
	if got != "https://mp.weixin.qq.com/s?__biz=abc&idx=1&mid=123&sn=xyz" {
		t.Fatal(got)
	}
}

func TestForwardedConversationRetainsNativeProjection(t *testing.T) {
	subtype := 19
	message := linuxnative.Message{
		Type:           49,
		AppMessageType: &subtype,
		Body:           "[Chat History] 测试聊天记录\n甲: 第一条测试文字\n乙: 第二条测试文字",
	}
	description := CardDescription(message)
	if text, coverage := historyMessageText(message); text != description || coverage != "forwarded_content" {
		t.Fatalf("history = %q, %q", text, coverage)
	}
	if !strings.Contains(description, "微信合并转发") || !strings.HasSuffix(description, message.Body) {
		t.Fatal(description)
	}
	if _, ok := PublisherCardDescription(message); ok {
		t.Fatal("forwarded conversation became publisher card copy")
	}
	message.Body = "[Chat History unavailable]"
	if description := CardDescription(message); !strings.Contains(description, "正文暂不可用") {
		t.Fatal(description)
	}
}

func TestHistoryKeepsNativeCardMetadataAndDiscardsMiniProgramLocator(t *testing.T) {
	subtype := 33
	fileNotice := 74
	for _, tc := range []struct {
		message  linuxnative.Message
		expected string
		coverage string
	}{
		{linuxnative.Message{Type: 42, Body: "显示名称：合成测试"}, "显示名称", "card_metadata_only"},
		{linuxnative.Message{Type: 48, Body: "地点：合成公园"}, "合成公园", "card_metadata_only"},
		{linuxnative.Message{Type: 49, AppMessageType: &subtype, Link: &linuxnative.LinkCard{Title: "合成小程序", URL: "https://example.org/PRIVATE"}}, "合成小程序", "card_metadata_only"},
		{linuxnative.Message{Type: 49, AppMessageType: &subtype}, "微信小程序卡片", "card_type_only"},
		{linuxnative.Message{Type: 49, AppMessageType: &fileNotice, Link: &linuxnative.LinkCard{Title: "示例.pdf"}}, "示例.pdf", "notice_only"},
		{linuxnative.Message{Type: 10000, Body: "合成系统提示"}, "合成系统提示", "notice_only"},
	} {
		text, coverage := historyMessageText(tc.message)
		if !strings.Contains(text, tc.expected) || strings.Contains(text, "PRIVATE") || coverage != tc.coverage {
			t.Errorf("kind %d history = %q, %q", tc.message.Type, text, coverage)
		}
	}
}
