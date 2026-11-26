package companion

import (
	"strings"
	"testing"
)

func TestLocalConversationPolicyKeepsSourcesWithoutApplicationProtocols(t *testing.T) {
	c := Defaults()
	e := &Engine{Config: c, system: "与本机主人交流"}
	local := e.policy(Input{Channel: LocalChannel})
	connected := e.policy(Input{Channel: "wechat"})
	for _, needed := range []string{c.Persona, c.Style, c.Relationship, "conversation_context", "来源", "版本", `"messages"`} {
		if !strings.Contains(local, needed) {
			t.Errorf("missing local policy component %q", needed)
		}
	}
	for _, needed := range []string{"未来事项与提醒", "agent_reminder", "agent_task"} {
		if !strings.Contains(connected, needed) {
			t.Errorf("missing connected temporal-action policy component %q", needed)
		}
	}
	for _, unavailable := range []string{"wechat_recall", "media_ids", "wake_after_seconds", "reply_contexts"} {
		if strings.Contains(local, unavailable) {
			t.Errorf("unconnected protocol entered local policy: %s", unavailable)
		}
	}
	if EstimateTokens(local)*100 >= EstimateTokens(connected)*60 {
		t.Fatalf("local policy did not remove unused application protocols: local=%d connected=%d", EstimateTokens(local), EstimateTokens(connected))
	}
	t.Logf("policy conservative estimate: local=%d connected=%d", EstimateTokens(local), EstimateTokens(connected))
}

func TestPolicyOmitsEmptyOptionalSectionsAndKeepsConfiguredContent(t *testing.T) {
	c := Defaults()
	c.PersonaDocument = false
	c.Appearance, c.Relationship = "", ""
	e := &Engine{Config: c, system: "base"}
	plain := e.policy(Input{Channel: "wechat"})
	for _, empty := range []string{"本群补充设定：", "可用图片素材（素材描述）：[]", "外观设定：\n", "相处方式：\n"} {
		if strings.Contains(plain, empty) {
			t.Fatalf("empty section reached model policy: %q", empty)
		}
	}
	if !strings.Contains(plain, "允许文字回复：") || !strings.Contains(plain, "人物设定："+c.Persona) {
		t.Fatal("required policy content was removed")
	}
	e.Images = []ImageAsset{{ID: "fixture-image", Title: "合成图"}}
	c.Permissions.SendImage = true
	e.Config = c
	withContent := e.policy(Input{Channel: "wechat", PersonaOverride: "合成群设定"})
	if !strings.Contains(withContent, "本群补充设定：合成群设定") || !strings.Contains(withContent, "fixture-image") {
		t.Fatal("configured group setting or image asset was removed")
	}
}
