package companion

import (
	"encoding/json"
	"testing"
	"wechat-bot/internal/identity"
)

func namedReplyFixture(t *testing.T) (*Engine, Input, Decision) {
	t.Helper()
	s, c, _ := memoryFixture(t)
	groundedInput(t, s, c, "beta", "other-member", "稍后联系。")
	in := groundedInput(t, s, c, "alpha", "current-member", "收到资料了。")
	in.SenderName = "小陈"
	if err := s.UpdateNames(in, c, map[string]identity.Name{"alpha": identity.Choose("alpha", "小陈", "", ""), "beta": identity.Choose("beta", "小周", "", "")}); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s, Config: c, model: &groundingSequenceModel{}}
	d, err := e.ReviewGroundedReply(t.Context(), in, Decision{Action: "reply", Messages: []string{"收到。"}})
	if err != nil {
		t.Fatal(err)
	}
	return e, in, d
}

func TestNamedAddressIsBoundToThisBubbleTargetOutsideQuotes(t *testing.T) {
	e, in, d := namedReplyFixture(t)
	for _, text := range []string{"小陈，收到。", "小周说稍后联系。", "他写道：“小周，稍后联系。”", "“小周，稍后联系。”是引文。", "他写道：'先等一下。\n小周，稍后联系。'", "> 小周，稍后联系。\n收到。"} {
		if err := e.ValidateReplyText(t.Context(), in, d.ReplyContexts[0], text); err != nil {
			t.Fatal("narration or quotation mistaken for outgoing address", text, err)
		}
	}
	for _, text := range []string{"小周，收到。", "[AI] 小周，收到。", "@beta: 收到。", "收到。小周：稍后联系。"} {
		if err := e.ValidateReplyText(t.Context(), in, d.ReplyContexts[0], text); err == nil || err.Error() != "reply_named_address_target_mismatch" {
			t.Fatal("explicit other member address ignored", text, err)
		}
	}
	if err := e.Store.UpdateNames(in, e.Config, map[string]identity.Name{"alpha": identity.Choose("alpha", "同名", "", ""), "beta": identity.Choose("beta", "同名", "", "")}); err != nil {
		t.Fatal(err)
	}
	in.SenderName = "同名"
	if err := e.ValidateReplyText(t.Context(), in, d.ReplyContexts[0], "同名，收到。"); err != nil {
		t.Fatal("ambiguous display name became an identity oracle", err)
	}
}

func TestModelRiskFlagsCannotExemptMemberAddressValidation(t *testing.T) {
	e, in, d := namedReplyFixture(t)
	r := d.ReplyContexts[0]
	r.RiskFlags = append(r.RiskFlags, "cross_conversation_delivery")
	if err := e.ValidateReplyContext(t.Context(), in, r); err != nil {
		t.Fatal("fixture must retain an otherwise valid source proof", err)
	}
	if err := e.ValidateReplyText(t.Context(), in, r, "小周，收到。"); err == nil || err.Error() != "reply_named_address_target_mismatch" {
		t.Fatal("model-provided advisory flag bypassed recipient binding", err)
	}
}

func TestNamedAddressMismatchCanBeRepairedOnceUsingOriginalRoute(t *testing.T) {
	e, in, _ := namedReplyFixture(t)
	model := &groundingSequenceModel{answers: []string{`{"action":"reply","bubbles":[{"text":"收到。","route_id":"R1"}]}`}}
	e.model = model
	out, err := e.ReviewGroundedReply(t.Context(), in, Decision{Action: "reply", Messages: []string{"小周，收到。"}})
	if err != nil || out.Action != "reply" || model.calls != 2 || out.ReplyContexts[0].TargetIDs[0] != "alpha" {
		t.Fatal(out, err, model.calls)
	}
	if err := e.ValidateReplyText(t.Context(), in, out.ReplyContexts[0], out.Messages[0]); err != nil {
		t.Fatal(err)
	}
}

func TestConversationDeliveryDoesNotBindDestinationVocativeToOriginRequester(t *testing.T) {
	e, in, d := namedReplyFixture(t)
	original := d.ReplyContexts[0].ContextVersion
	in.ConversationDelivery = true
	if e.ValidateReplyContext(t.Context(), in, d.ReplyContexts[0]) == nil {
		t.Fatal("member proof was silently changed into conversation delivery")
	}
	d, err := e.ReviewGroundedReply(t.Context(), in, Decision{Action: "reply", Messages: []string{"小周，收到。"}})
	if err != nil || d.ReplyContexts[0].ContextVersion == original {
		t.Fatal(d, err)
	}
	if err := e.ValidateReplyText(t.Context(), in, d.ReplyContexts[0], "小周，收到。"); err != nil {
		t.Fatal("origin requester mistaken for destination audience", err)
	}
	rows := replyAudiencesForInput(in, d)
	if rows[0]["outgoing_you_means"] != nil || len(rows[0]["target_ids"].([]string)) != 0 {
		t.Fatal(rows)
	}
	raw, _ := json.Marshal(in)
	var restored Input
	_ = json.Unmarshal(raw, &restored)
	if !restored.ConversationDelivery || ReplyContextFingerprint(restored, d.ReplyContexts[0]) != d.ReplyContexts[0].ContextVersion {
		t.Fatal("delivery kind lost across durable serialization")
	}
}
