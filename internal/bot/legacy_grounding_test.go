package bot

import (
	"context"
	"encoding/json"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func sourcedReplyFixture(t *testing.T) (*Worker, *companion.Engine, companion.Input, companion.ReplyContext) {
	w, c, _, in := planWorker(t, &planReviewModel{answer: `{"action":"reply","messages":["收到你的消息"],"reply_contexts":[]}`})
	engine, err := companion.New(c.Config, c.Store, legacyReplyCheckModel{}, "synthetic-check")
	if err != nil {
		t.Fatal(err)
	}
	c = engine
	w.model = c
	in.Thread, in.IsGroup, in.Text = "room@chatroom", true, "合成来源正文"
	in.MessageTime = time.Now().UTC().Format(time.RFC3339)
	original := in.Text
	in.OriginalText = &original
	in.Source = &companion.SourceRef{InstanceID: c.Store.SourceInstanceID(), MessageID: in.TraceID, ServerID: "77", OccurredAt: in.MessageTime, Revision: 1}
	if err := c.Store.Observe(in, c.Config, time.Now()); err != nil {
		t.Fatal(err)
	}
	source, ok := c.Store.SourceForInput(in)
	if !ok {
		t.Fatal("source unavailable")
	}
	in.Source = source
	r := companion.ReplyContext{TargetIDs: []string{in.Sender}, AnchorMessageID: in.TraceID, AnchorEventID: source.EventID, SourceEventIDs: []string{source.EventID}, SourceVersions: map[string]int{source.EventID: 1}, State: "verified", ContextVersion: "synthetic-verified"}
	return w, c, in, r
}

type legacyReplyCheckModel struct{}

func (legacyReplyCheckModel) Chat(_ context.Context, _ string, turns []map[string]string) (string, error) {
	var packet struct {
		Candidate companion.Decision              `json:"candidate"`
		Units     []companion.GroundingReviewUnit `json:"review_units"`
	}
	if len(turns) > 0 && json.Unmarshal([]byte(turns[0]["content"]), &packet) == nil && len(packet.Candidate.Messages) > 0 {
		units := []map[string]any{}
		for _, unit := range packet.Units {
			units = append(units, map[string]any{"unit_id": unit.ID, "atoms": []map[string]any{{"span": unit.Text, "mode": "nonfactual", "claim": "", "status": "nonfactual", "nonfactual_category": "acknowledgment", "supports": []any{}, "reason": "Synthetic acknowledgment fixture"}}})
		}
		raw, _ := json.Marshal(map[string]any{"units": units})
		return string(raw), nil
	}
	return `{"action":"reply","messages":["收到你的消息"]}`, nil
}

func TestLegacyReplyMissingRevisionResolvesOnlyUnchangedOriginal(t *testing.T) {
	w, _, in, r := sourcedReplyFixture(t)
	r.SourceVersions = nil
	in.ReplyContext = &r
	got, _, err := w.prepareGroundedText(context.Background(), in, "收到你的消息")
	if err != nil || got.ReplyContext.SourceVersions[in.Source.EventID] != 1 {
		t.Fatal("unchanged original could not be re-reviewed", err)
	}
}

func TestLegacyReplyMissingSourceRejectsDifferentOriginal(t *testing.T) {
	w, _, in, r := sourcedReplyFixture(t)
	in.Source = nil
	old := "另一个旧正文"
	in.OriginalText = &old
	in.ReplyContext = &r
	if _, _, err := w.prepareGroundedText(context.Background(), in, "收到"); err == nil {
		t.Fatal("legacy wrong original was rebound")
	}
}

func TestLegacyReplyCannotUpgradeRevisedSource(t *testing.T) {
	w, c, in, r := sourcedReplyFixture(t)
	changed := in
	s := *in.Source
	s.Revision = 2
	changed.Source = &s
	changed.Text = "更正后的来源"
	body := changed.Text
	changed.OriginalText = &body
	if err := c.Store.Observe(changed, c.Config, time.Now()); err != nil {
		t.Fatal(err)
	}
	r.SourceVersions = nil
	in.ReplyContext = &r
	if _, _, err := w.prepareGroundedText(context.Background(), in, "旧回复"); err == nil {
		t.Fatal("old draft silently upgraded its source revision")
	}
}

func TestLegacyReplyCannotRecoverForgottenSource(t *testing.T) {
	w, c, in, r := sourcedReplyFixture(t)
	if err := c.Store.Clear(companion.ScopeID(in.Channel, in.Account, in.Thread)); err != nil {
		t.Fatal(err)
	}
	r.State = ""
	in.ReplyContext = &r
	if _, _, err := w.prepareGroundedText(context.Background(), in, "旧回复"); err == nil {
		t.Fatal("forgotten source was recovered")
	}
}

func TestLegacyReplyWithoutAnyBindingCannotBareSend(t *testing.T) {
	w, _, _, in := planWorker(t, &planReviewModel{})
	in.Thread, in.IsGroup = "room@chatroom", true
	if _, _, err := w.prepareGroundedText(context.Background(), in, "无来源旧计划"); err == nil {
		t.Fatal("unresolved legacy draft allowed")
	}
}

func TestEphemeralSourceRequiresNativeIdentityBodyAndTime(t *testing.T) {
	w, _, _, in := planWorker(t, &planReviewModel{})
	m := linuxnative.Message{ID: in.TraceID, AccountID: in.Account, ThreadID: "room@chatroom", SenderID: in.Sender, ServerID: "verified-server", Body: "stateless synthetic text", Type: 1, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	s := messageSource(m)
	in.Source = &s
	in.Thread = m.ThreadID
	w.source = &fakeSource{messages: map[string]linuxnative.Message{m.ID: m}}
	got, err := w.verifyEphemeralSource(context.Background(), in)
	if err != nil || got.Source.Integrity != "verified" {
		t.Fatal(err)
	}
	m.Body = "changed body"
	w.source = &fakeSource{messages: map[string]linuxnative.Message{m.ID: m}}
	if _, err = w.verifyEphemeralSource(context.Background(), in); err == nil {
		t.Fatal("stateless old body upgraded")
	}
	in.Source = nil
	if _, err = w.verifyEphemeralSource(context.Background(), in); err == nil {
		t.Fatal("missing old source admitted as stateless")
	}
}

func TestLegacyMissingEvidenceEpochRequiresRevalidation(t *testing.T) {
	w, _, in, r := sourcedReplyFixture(t)
	in.ReplyContext = &r
	got, _, err := w.prepareGroundedText(context.Background(), in, "收到你的消息")
	if err != nil || got.ReplyContext.EvidenceEpoch == "" {
		t.Fatal("legacy epoch was not rechecked", err)
	}
}

func TestStaleEvidenceEpochCannotBeResignedThroughMissingRevision(t *testing.T) {
	w, _, in, r := sourcedReplyFixture(t)
	r.EvidenceEpoch = "stale-epoch"
	r.SourceVersions = nil
	in.ReplyContext = &r
	if _, _, err := w.prepareGroundedText(context.Background(), in, "旧回复"); err == nil {
		t.Fatal("stale epoch resigned")
	}
}

type legacyEvidenceTools struct {
	companion.ToolAccess
	stamp string
}

func TestGenerationToolsForwardChatAuthorizationAndRevocation(t *testing.T) {
	adapter := &legacyEvidenceTools{stamp: "original"}
	run := &generationTools{inner: adapter}
	stamp := run.MemoryResultAuthorization()
	if stamp != "original" || !run.MemoryResultAuthorized(stamp) {
		t.Fatal("ordinary chat lost its host authorization")
	}
	adapter.stamp = "changed"
	if run.MemoryResultAuthorized(stamp) || run.MemoryResultAuthorized("") {
		t.Fatal("a changed grant reauthorized an old query")
	}
}

func (p *legacyEvidenceTools) MemoryResultAuthorization() string { return p.stamp }
func (p *legacyEvidenceTools) MemoryResultAuthorized(stamp string) bool {
	return stamp != "" && stamp == p.stamp
}

func TestReplyEvidenceAuthorizationCannotBeRenewedAfterRevocation(t *testing.T) {
	w, c, in, r := sourcedReplyFixture(t)
	c.Tools = &legacyEvidenceTools{stamp: "new-authority"}
	r.EvidenceAuthorization = "old-authority"
	in.ReplyContext = &r
	if _, _, err := w.prepareGroundedText(context.Background(), in, "旧回复"); err == nil {
		t.Fatal("revoked authorization renewed")
	}
}

func TestLegacyMissingAuthorizationRequiresCurrentReadOnlyRevalidation(t *testing.T) {
	w, c, in, r := sourcedReplyFixture(t)
	c.Tools = &legacyEvidenceTools{stamp: "active-authority"}
	epoch, err := c.Store.RecallEpoch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r.EvidenceEpoch = epoch
	in.ReplyContext = &r
	got, _, err := w.prepareGroundedText(context.Background(), in, "收到你的消息")
	if err != nil || got.ReplyContext.EvidenceAuthorization != "active-authority" {
		t.Fatal("missing authority did not trigger review", err)
	}
}

func TestQuoteAuthorUsesVerifiedSourceChainRatherThanAudienceMembership(t *testing.T) {
	for _, tc := range []struct {
		name, author, anchor string
		allowed              bool
	}{
		{"source-author-differs-from-target", "alpha", "other-anchor", true},
		{"native-author-mismatch", "wrong-member", "other-anchor", false},
		{"unrelated-latest-target-message", "one", "unrelated-latest", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, _, _, in := planWorker(t, &planReviewModel{})
			w.model = &groundingModelProbe{}
			in.Thread, in.IsGroup = "room@chatroom", true
			in.Source = &companion.SourceRef{EventID: "synthetic-event", MessageID: in.TraceID, Revision: 1, BodyHash: "synthetic"}
			in.QuotedSource = &companion.SourceRef{EventID: "anchor-event", MessageID: "other-anchor", Account: in.Account, Thread: in.Thread, Sender: "alpha", ServerID: "server", Revision: 1}
			in.ReplyContext = &companion.ReplyContext{TargetIDs: []string{in.Sender}, AnchorMessageID: tc.anchor, AnchorEventID: "anchor-event", SourceEventIDs: []string{"anchor-event"}, SourceVersions: map[string]int{"anchor-event": 1}, State: "verified", ContextVersion: "synthetic", QuoteRequired: true}
			w.source = &fakeSource{messages: map[string]linuxnative.Message{tc.anchor: {ID: tc.anchor, AccountID: in.Account, ThreadID: in.Thread, ServerID: "server", SenderID: tc.author}}}
			_, _, err := w.prepareGroundedText(context.Background(), in, "合成引用回复")
			if (err == nil) != tc.allowed {
				t.Fatal("quote source-chain validation", tc, err)
			}
		})
	}
}
