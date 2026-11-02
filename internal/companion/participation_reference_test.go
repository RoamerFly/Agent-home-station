package companion

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func participationFixture(t *testing.T, current, quoted, olderSelf string) (*Engine, Input, Decision) {
	t.Helper()
	s, c, _ := memoryFixture(t)
	base := Input{Channel: "wechat", Account: "participants", Thread: "family@chatroom", Sender: "alpha", Mode: "direct", IsGroup: true, Directed: true}
	observe := func(sender, id, text string) Input {
		in := base
		in.Sender, in.Text = sender, text
		in = syntheticNativeInput(in, id)
		if err := s.Observe(in, c, time.Now()); err != nil {
			t.Fatal(err)
		}
		ref, ok := s.SourceForInput(in)
		if !ok {
			t.Fatal("no source")
		}
		in.Source = ref
		return in
	}
	if olderSelf != "" {
		observe("alpha", "unrelated-older-call", olderSelf)
	}
	var quote *SourceRef
	if quoted != "" {
		q := observe("beta", "quoted-call", quoted)
		quote = q.Source
	}
	in := base
	in.Text = current
	in.Addressees = []string{"beta"}
	in.QuotedSource = quote
	in = syntheticNativeInput(in, "current-report")
	if err := s.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	in.Source, _ = s.SourceForInput(in)
	authority := &memoryAuthorizationFixture{stamp: "participation-grant"}
	e := &Engine{Store: s, Config: c, Tools: authority, model: &groundingSequenceModel{}}
	d, err := e.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{"收到。"}})
	if err != nil {
		t.Fatal(err)
	}
	d, err = e.bindReplyEvidence(t.Context(), in, d)
	if err != nil {
		t.Fatal(err)
	}
	return e, in, d
}

func TestParticipationReferenceDoesNotMakeReporterACallParticipant(t *testing.T) {
	e, in, d := participationFixture(t, "你爸刚视频，刚挂掉。", "", "我刚和朋友通话结束。")
	for _, text := range []string{"那你们聊得怎么样？", "您们聊得如何？", "你俩刚视频结束了吧。", "你们没聊好吧。", "你们不是刚聊完吗？", "你们没聊得很开心吧。"} {
		if err := e.ValidateReplyText(t.Context(), in, d.ReplyContexts[0], text); err == nil || err.Error() != "reply_participation_reference_required" {
			t.Fatal("reporter became participant", text, err)
		}
	}
	for _, text := range []string{
		"你们说的是乙与父亲那通视频。", "你说的是乙与父亲那通视频。",
		"如果你们刚聊完，可以以后再说。", "你们下次可以聊聊。",
		"她原话是“你们聊得怎么样？”", "`你们刚聊完`是待检查的旧话。", "> 你们聊得怎么样？\n这是引文。",
		"是你们刚通话吗？", "你们没通话。", "你们没有通话。", "你们不是在说乙那通视频吗？",
	} {
		if err := e.ValidateReplyText(t.Context(), in, d.ReplyContexts[0], text); err != nil {
			t.Fatal("report, condition, quote or clarification was mistaken for participation", text, err)
		}
	}
	evidence := e.participationReferenceEvidence(t.Context(), in, d.ReplyContexts[0])
	if !evidence.Active || evidence.Source.MessageID != in.TraceID || evidence.Source.Revision <= 0 || len(evidence.EstablishedParticipantIDs) != 0 {
		t.Fatal("old self call supplied focal participation", evidence)
	}
	roles := e.currentReferenceRoles(t.Context(), in, d)
	if len(roles) != 1 || roles[0]["reporter_role_does_not_establish_participation_or_personal_experience"] != true {
		t.Fatal(roles)
	}
}

func TestParticipationReferenceUsesOnlyExplicitQuotedEventForSelfSupport(t *testing.T) {
	for _, tc := range []struct {
		quote         string
		betaSupported bool
	}{
		{"我刚与自己的父亲视频结束。", true},
		{"我爸刚打完电话。", false},
		{"我没通电话。", false},
		{"如果我刚和家人视频结束，那以后再说。", false},
		{"我刚和家人视频结束了吗？", false},
		{"我爸刚视频结束。我刚和同事通话。", false},
	} {
		t.Run(tc.quote, func(t *testing.T) {
			e, in, d := participationFixture(t, "我引用这条资料，你核对一下。", tc.quote, "我刚和朋友通话结束。")
			if err := e.ValidateReplyText(t.Context(), in, d.ReplyContexts[0], "你们聊得怎么样？"); err == nil {
				t.Fatal("an unrelated self call licensed this quoted event")
			}
			base := Decision{Action: "reply", Messages: []string{"收到。"}, ReplyContexts: []ReplyContext{{TargetIDs: []string{"beta"}, SourceEventIDs: []string{in.Source.EventID, in.QuotedSource.EventID}}}}
			toBeta, err := e.normalizeReplyContexts(t.Context(), in, base)
			if err != nil {
				t.Fatal(err)
			}
			toBeta, err = e.bindReplyEvidence(t.Context(), in, toBeta)
			if err != nil {
				t.Fatal(err)
			}
			err = e.ValidateReplyText(t.Context(), in, toBeta.ReplyContexts[0], "你们聊得怎么样？")
			if (err == nil) != tc.betaSupported {
				t.Fatal("ownership, question, hypothesis or negation became positive participation", tc, err, e.participationReferenceEvidence(t.Context(), in, toBeta.ReplyContexts[0]))
			}
		})
	}
}

func TestParticipationReferenceDoesNotMergeDifferentCallsInOneMessage(t *testing.T) {
	for _, source := range []string{"你爸刚视频，刚挂掉。我刚和同事通话。", "我刚和同事通话。你爸刚视频，刚挂掉。"} {
		e, in, d := participationFixture(t, source, "", "")
		if err := e.ValidateReplyText(t.Context(), in, d.ReplyContexts[0], "你们聊得怎么样？"); err == nil {
			t.Fatal("another clause's self-call became the relative's call participation", source)
		}
		if got := e.participationReferenceEvidence(t.Context(), in, d.ReplyContexts[0]); len(got.EstablishedParticipantIDs) != 0 {
			t.Fatal(got)
		}
	}
	e, in, d := participationFixture(t, "我刚和我爸视频结束。", "", "")
	if err := e.ValidateReplyText(t.Context(), in, d.ReplyContexts[0], "你们聊得怎么样？"); err != nil {
		t.Fatal("explicit self participation in this same clause was lost", err, e.participationReferenceEvidence(t.Context(), in, d.ReplyContexts[0]))
	}
}

func TestParticipationReferenceCanRepairByExistingRouteWithoutChangingProof(t *testing.T) {
	e, in, d := participationFixture(t, "我引用这条资料，你核对一下。", "我刚与父亲视频结束。", "")
	model := &groundingSequenceModel{answers: []string{`{"action":"reply","bubbles":[{"text":"你们聊得怎么样？","route_id":"R2"}]}`}}
	e.model = model
	d.Messages = []string{"那你们聊得怎么样？"}
	out, err := e.DiagnoseGroundedReply(t.Context(), in, d)
	if err != nil || out.Action != "reply" || model.calls != 2 || out.ReplyContexts[0].TargetIDs[0] != "beta" || !out.ReplyContexts[0].QuoteRequired || out.ReplyContexts[0].AnchorMessageID != in.QuotedSource.MessageID {
		t.Fatal(out, err, model.calls)
	}
	if err = e.ValidateReplyText(t.Context(), in, out.ReplyContexts[0], out.Messages[0]); err != nil {
		t.Fatal(err)
	}
	if model.inputs[0]["program_contract_violations"] == nil || model.inputs[0]["current_role_evidence"] == nil {
		t.Fatal("editor lacked the actual program source constraint", model.inputs[0])
	}
}

func TestParticipationProgramContractRejectsModelAllowAndPreservesOriginal(t *testing.T) {
	e, in, d := participationFixture(t, "你爸刚视频，刚挂掉。", "", "")
	d.Messages = []string{"那你们聊得怎么样？"}
	before, _ := json.Marshal(d)
	verdict, err := e.checkReplyGrounding(t.Context(), in, d)
	if err != nil || verdict.Verdict != "reject" || verdict.Bubbles[0].Violations[0].Kind != "wrong_person" {
		t.Fatal(verdict, err)
	}
	after, _ := json.Marshal(d)
	if string(before) != string(after) {
		t.Fatal("program check rewrote a candidate")
	}
	if !strings.Contains(string(verdict.ReviewBinding.ReviewInput), "current_role_evidence") || !strings.Contains(string(verdict.ReviewBinding.ReviewInput), "source_reference_contracts") {
		t.Fatal("checker missed the current source contract")
	}
	for _, mutate := range []string{"revoke", "forget"} {
		if mutate == "revoke" {
			e.Tools.(*memoryAuthorizationFixture).stamp = "revoked"
		} else {
			_ = e.Store.Clear(ScopeID(in.Channel, in.Account, in.Thread))
		}
		if err = e.ValidateReplyText(t.Context(), in, d.ReplyContexts[0], "你们聊得怎么样？"); err == nil {
			t.Fatal("source guard bypassed revoked/forgotten proof", mutate)
		}
	}
}
