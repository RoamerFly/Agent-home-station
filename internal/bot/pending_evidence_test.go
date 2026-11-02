package bot

import (
	"context"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func pendingSecondaryFixture(t *testing.T) (*Worker, *companion.Engine, companion.Input, *planReviewModel, func()) {
	t.Helper()
	m := &planReviewModel{answer: `{"action":"reply","messages":["参考了合成次要资料"]}`}
	w, e, _, in := planWorker(t, m)
	secondary := in
	secondary.TraceID, secondary.Text = "secondary-memory-source", "合成次要记忆内容"
	secondary = observeSyntheticMediaRequest(t, w, e, secondary)
	src, ok := e.Store.SourceForInput(secondary)
	if !ok {
		t.Fatal("secondary source missing")
	}
	entry, err := e.Store.SaveMemory(context.Background(), companion.MemoryEntry{Scope: companion.ScopeID(in.Channel, in.Account, in.Thread), Subject: in.Sender, Kind: "fact", Detail: secondary.Text, Sources: []companion.MemorySource{{EventID: src.EventID, Quote: secondary.Text}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	in.ReplyContext = nil
	d, err := e.Respond(context.Background(), in)
	if err != nil || len(d.ReplyContexts) != 1 {
		t.Fatal("actual original decision missing proof", err, d)
	}
	in.ReplyContext = &d.ReplyContexts[0]
	forget := func() {
		entry.Status = "deleted"
		if _, err := e.Store.SaveMemory(context.Background(), entry, entry.Version); err != nil {
			t.Fatal(err)
		}
		source, ok := e.Store.SourceForInput(in)
		if !ok {
			t.Fatal("forget incorrectly removed the independent trigger")
		}
		if _, err := e.Store.ReadEvent(context.Background(), []string{entry.Scope}, source.EventID); err != nil {
			t.Fatal("original trigger no longer readable", err)
		}
	}
	return w, e, in, m, forget
}

func TestLegacyPrivateDraftCannotBeResignedFromSurvivingAnchor(t *testing.T) {
	for _, shape := range []string{"nil-context", "missing-epoch"} {
		t.Run(shape, func(t *testing.T) {
			w, _, in, m, forget := pendingSecondaryFixture(t)
			if shape == "nil-context" {
				in.ReplyContext = nil
			} else {
				r := *in.ReplyContext
				r.EvidenceEpoch = ""
				in.ReplyContext = &r
			}
			forget()
			before := m.calls
			if _, _, err := w.prepareGroundedText(context.Background(), in, "依赖已忘次要记忆的旧草稿"); err == nil {
				t.Fatal("legacy private draft silently acquired current evidence")
			}
			if m.calls != before {
				t.Fatal("legacy draft was treated as a fresh private generation")
			}
		})
	}
}

func TestPendingReplacementRetainsReviewEvidenceAcrossRestart(t *testing.T) {
	for _, action := range []string{"replace", "delay"} {
		t.Run(action, func(t *testing.T) {
			w, e, in, m, forget := pendingSecondaryFixture(t)
			m.answer = `{"action":"` + action + `","messages":["审核生成的新草稿"],"delay_seconds":30}`
			w.queueUtterancePlan(e, in, companion.Decision{Action: "reply", Messages: []string{"旧草稿"}, ReplyContexts: []companion.ReplyContext{*in.ReplyContext}})
			p := w.runtime.Plans[0]
			r, err := w.reviewPlan(context.Background(), e, p)
			if err != nil {
				t.Fatal(err)
			}
			w.applyPlanReview(p, r, nil)
			if p.ReviewEvidence == nil || p.Input.ReplyContext != nil || len(p.ReplyContexts) != 0 {
				t.Fatal("rewrite did not retain only its original review stamp")
			}
			if err := w.loadRuntime("server-1"); err != nil {
				t.Fatal(err)
			}
			p = w.runtime.Plans[0]
			forget()
			p.Next = time.Time{}
			readyPlans(w)
			w.reconcileUtterancePlans(context.Background())
			if len(w.runtime.Deliveries) != 0 || p.State != "stopped" {
				t.Fatal("reviewed private draft was rebound after forgetting", p.State, w.runtime.Deliveries)
			}
		})
	}
}

func TestUncertainDeliveryRetainsEvidenceAndNeverRewritesAcrossRestart(t *testing.T) {
	w, _, in, m, forget := pendingSecondaryFixture(t)
	n := &reliableFake{intents: map[string]linuxnative.ToolStatus{}}
	w.sender = n
	_, _ = w.sendText(context.Background(), in, 0, "原未知回执草稿")
	d := w.runtime.Deliveries[0]
	beforeCalls := m.calls
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	d = w.runtime.Deliveries[0]
	forget()
	before := len(n.submissions)
	d.Next = time.Time{}
	w.runtime.SendGates = nil
	w.reconcileDeliveries(context.Background())
	if d.Text != "原未知回执草稿" || m.calls != beforeCalls || len(n.submissions) != before || d.State == "accepted" {
		t.Fatal("unknown delivery rewrote or resubmitted an expired draft", d, m.calls, n.submissions)
	}
}
