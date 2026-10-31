package bot

import (
	"context"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func TestAmbientGroupMessageCannotRewriteAnotherMembersPlan(t *testing.T) {
	w, c, _, in := planWorker(t, &planReviewModel{})
	in.Thread, in.IsGroup, in.Sender = "room@chatroom", true, "member-a"
	in.Directed = false
	w.queueUtterancePlan(c, in, companion.Decision{Action: "reply", Messages: []string{"这份饭先吃完"}})
	m := fixtureMessage("goodnight", "room@chatroom", "晚安咯", "incoming")
	m.SenderID = "member-b"
	w.noticeConversationMessage(m, true)
	p := w.runtime.Plans[0]
	if p.State != "queued" || len(p.Updates) != 0 || p.Quote == nil || p.Quote.MessageID != in.TraceID {
		t.Fatal("ambient message changed old target or failed to quote", p)
	}
	m.SenderID = in.Sender
	m.ID = "caption"
	m.Body = "这是盒饭"
	w.noticeConversationMessage(m, true)
	if p.State != "review_pending" || len(p.Updates) != 1 || p.Updates[0].Source == nil || p.Updates[0].Sender != in.Sender {
		t.Fatal("same sender supplement lost source", p)
	}
}

func TestInputSourcePreservesImageUploaderAndQuotedAuthor(t *testing.T) {
	_, c, _, in := planWorker(t, &planReviewModel{})
	in.Thread, in.Sender = "room@chatroom", "member-a"
	m := fixtureMessage("picture", "room@chatroom", "", "incoming")
	m.AccountID = in.Account
	m.SenderID = in.Sender
	m.Type = 3
	m.ResolvedQuote = &linuxnative.Message{ID: "old", AccountID: in.Account, ThreadID: in.Thread, SenderID: "member-b", CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	bindInputSource(&in, m, c)
	if in.Source == nil || in.Source.MessageID != m.ID || in.QuotedSource == nil || in.QuotedSource.Sender != "member-b" || len(in.IncomingMedia) != 1 || in.IncomingMedia[0].UploaderID != "member-a" {
		t.Fatal(in)
	}
	if in.IncomingMedia[0].ObservationState != "pending" {
		t.Fatal("unread image marked observed")
	}
}

func TestRewrittenPlanCannotReuseOriginalTextGrounding(t *testing.T) {
	w, c, _, in := planWorker(t, &planReviewModel{answer: `{"action":"replace","messages":["新文"]}`})
	w.queueUtterancePlan(c, in, companion.Decision{Messages: []string{"原文"}, ReplyContexts: []companion.ReplyContext{*in.ReplyContext}})
	p := w.runtime.Plans[0]
	r, err := w.reviewPlan(context.Background(), c, p)
	if err != nil {
		t.Fatal(err)
	}
	w.applyPlanReview(p, r, nil)
	if len(p.ReplyContexts) != 0 {
		t.Fatal("new draft inherited stale grounding", p.ReplyContexts)
	}
}

func TestOtherMemberQuotedMessageCannotCancelPersistentTask(t *testing.T) {
	w, c, _, in := planWorker(t, &planReviewModel{})
	in.Thread, in.IsGroup, in.TaskID = "room@chatroom", true, "owned-task"
	w.queueUtterancePlan(c, in, companion.Decision{Messages: []string{"处理原请求"}})
	p := w.runtime.Plans[0]
	p.Updates = []companion.ConversationEvent{{MessageID: "other-quote", Sender: "other", Text: "别做了", Relation: "verified_quote"}}
	w.applyPlanReview(p, companion.PendingReview{Action: "cancel", EvidenceMessageID: "other-quote"}, nil)
	if p.State == "stopped" {
		t.Fatal("other member cancelled owned task")
	}
}

type groundingModelProbe struct {
	calls   int
	invalid bool
}

func (m *groundingModelProbe) Complete(context.Context, string) (string, error) { return "", nil }

func (m *groundingModelProbe) Chat(context.Context, string, []map[string]string) (string, error) {
	return "", nil
}
func (m *groundingModelProbe) ValidateReplyContext(context.Context, companion.Input, companion.ReplyContext) error {
	m.calls++
	if m.invalid {
		return context.Canceled
	}
	return nil
}
func (m *groundingModelProbe) ReviewGroundedReply(_ context.Context, in companion.Input, d companion.Decision) (companion.Decision, error) {
	d.ReplyContexts = []companion.ReplyContext{{TargetIDs: []string{in.Sender}, AnchorMessageID: in.TraceID, ContextVersion: "synthetic", State: "verified", SourceEventIDs: []string{"synthetic-event"}, SourceVersions: map[string]int{"synthetic-event": 1}}}
	return d, nil
}

func TestGroundingFailureBlocksNativeSubmissionButNotReceiptLookup(t *testing.T) {
	w, _, n, in := planWorker(t, &planReviewModel{})
	m := &groundingModelProbe{invalid: true}
	w.model = m
	in.Thread, in.IsGroup = "room@chatroom", true
	in.Source = &companion.SourceRef{EventID: "synthetic-event", MessageID: in.TraceID, Revision: 1, BodyHash: "synthetic-hash"}
	_, err := w.sendText(context.Background(), in, 0, "合成回复")
	if err == nil || len(n.submissions) != 0 {
		t.Fatal("invalid grounding reached native", err, n.submissions)
	}
	m.invalid = false
	if _, err = w.sendText(context.Background(), in, 0, "合成回复"); err != nil {
		t.Fatal(err)
	}
	if len(n.submissions) != 1 {
		t.Fatal(n.submissions)
	}
	m.invalid = true
	if _, err = w.sendText(context.Background(), in, 0, "合成回复"); err != nil || len(n.submissions) != 1 {
		t.Fatal("accepted reply resubmitted or rejected from late validation", err, n.submissions)
	}
}
