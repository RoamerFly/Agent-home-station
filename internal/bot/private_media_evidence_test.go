package bot

import (
	"context"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func observeSyntheticMediaRequest(t *testing.T, w *Worker, e *companion.Engine, in companion.Input) companion.Input {
	t.Helper()
	if in.Sender == "" {
		in.Sender = "synthetic-requester"
	}
	if in.TraceID == "" {
		in.TraceID = "synthetic-request"
	}
	m := fixtureMessage(in.TraceID, in.Thread, in.Text, "incoming")
	m.SenderID = in.Sender
	m.ServerID = "synthetic-native-" + in.TraceID
	in.MessageTime, in.OriginalText, in.ReplyContext = m.CreatedAt, &m.Body, nil
	bindInputSource(&in, m, e)
	if native, ok := w.source.(*fakeSource); ok {
		if native.messages == nil {
			native.messages = map[string]linuxnative.Message{}
		}
		native.messages[m.ID] = m
	}
	original := in
	original.TaskID, original.BackgroundResult, original.ReportOnly = "", false, false
	if err := e.Store.Observe(original, e.Config, time.Now()); err != nil {
		t.Fatal(err)
	}
	if e.Config.Permissions.Remember {
		r, err := e.BindReplyEvidence(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		in.ReplyContext = &r
	}
	return in
}

func privateProofFixture(t *testing.T) (*Worker, *companion.Engine, companion.Input, *planReviewModel) {
	t.Helper()
	m := &planReviewModel{answer: `{"action":"reply","messages":["收到你的请求"]}`}
	w, e, _, in := planWorker(t, m)
	e.Tools = &legacyEvidenceTools{ToolAccess: generatedTools{}, stamp: "synthetic-current-permission"}
	d, err := e.Respond(context.Background(), in)
	if err != nil || len(d.ReplyContexts) != 1 || d.ReplyContexts[0].EvidenceEpoch == "" || d.ReplyContexts[0].EvidenceAuthorization == "" {
		t.Fatal("actual private decision missing evidence proof", err, d)
	}
	in.ReplyContext = &d.ReplyContexts[0]
	return w, e, in, m
}

func changePrivateEvidence(t *testing.T, e *companion.Engine, in companion.Input, mutation string) {
	t.Helper()
	switch mutation {
	case "forget":
		if err := e.Store.Clear(companion.ScopeID(in.Channel, in.Account, in.Thread)); err != nil {
			t.Fatal(err)
		}
	case "revise":
		s, ok := e.Store.SourceForInput(in)
		if !ok {
			t.Fatal("original source missing")
		}
		changed := in
		changed.ReplyContext = nil
		s.Revision++
		changed.Source = s
		text := "用户已更正这个请求"
		changed.Text, changed.OriginalText = text, &text
		if err := e.Store.Observe(changed, e.Config, time.Now()); err != nil {
			t.Fatal(err)
		}
	case "revoke":
		e.Tools.(*legacyEvidenceTools).stamp = "synthetic-revoked-permission"
	}
}

func TestPrivateActualDecisionProofIsBoundWithoutExtraModelCall(t *testing.T) {
	w, _, in, m := privateProofFixture(t)
	before := m.calls
	if _, _, err := w.prepareGroundedText(context.Background(), in, "收到你的请求"); err != nil {
		t.Fatal(err)
	}
	if m.calls != before || before != 1 {
		t.Fatal("private proof added semantic model calls", before, m.calls)
	}
}

func TestPrivateSignedDecisionCanRestoreItsExactSourceProjection(t *testing.T) {
	for _, changed := range []bool{false, true} {
		w, _, in, _ := privateProofFixture(t)
		in.Source, in.MessageTime = nil, ""
		if changed {
			text := "different unverified original"
			in.OriginalText = &text
		}
		got, _, err := w.prepareGroundedText(context.Background(), in, "收到你的请求")
		if changed && err == nil {
			t.Fatal("proof rebound to a different literal")
		}
		if !changed && (err != nil || got.Source == nil || got.MessageTime == "") {
			t.Fatal("signed source projection could not be restored", err)
		}
	}
}

func TestRememberDisabledNativeReplyIsVerifiedBeforeGeneration(t *testing.T) {
	model := &planReviewModel{answer: `{"action":"reply","messages":["收到你的请求"]}`}
	w, e, n := backgroundWorker(t, model)
	records := []Record{}
	w.config.Record = func(r Record) { records = append(records, r) }
	e.Config.Permissions.Remember = false
	m := fixtureMessage("ephemeral-native-request", "filehelper", "/ai 合成请求", "incoming")
	m.SenderID, m.ServerID = "native-requester", "native-server-123"
	w.source = &fakeSource{messages: map[string]linuxnative.Message{m.ID: m}}
	w.handle(context.Background(), fixtureEvent("1", m.ID, m.ThreadID, "incoming", "wechat"))
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	if model.calls != 1 || len(n.submissions) != 1 {
		t.Fatal("native stateless request lost or gained review calls", model.calls, len(n.submissions), records)
	}
}

func TestPrivateActualDecisionRejectsStaleEvidence(t *testing.T) {
	for _, mutation := range []string{"forget", "revise", "revoke"} {
		t.Run(mutation, func(t *testing.T) {
			w, e, in, _ := privateProofFixture(t)
			changePrivateEvidence(t, e, in, mutation)
			if _, _, err := w.prepareGroundedText(context.Background(), in, "旧私聊回复"); err == nil {
				t.Fatal("stale private decision allowed")
			}
		})
	}
}

func TestPrivateAcceptedReceiptSurvivesEvidenceInvalidation(t *testing.T) {
	for _, mutation := range []string{"forget", "revise", "revoke"} {
		t.Run(mutation, func(t *testing.T) {
			w, e, in, _ := privateProofFixture(t)
			n := w.sender.(*acceptingProgressNative)
			if _, err := w.sendText(context.Background(), in, 0, "已接受的回复"); err != nil {
				t.Fatal(err)
			}
			changePrivateEvidence(t, e, in, mutation)
			if _, err := w.sendText(context.Background(), in, 0, "已接受的回复"); err != nil {
				t.Fatal("accepted evidence fact lost", err)
			}
			if len(n.submissions) != 1 {
				t.Fatal("accepted message resent", len(n.submissions))
			}
		})
	}
}

func TestNormalMediaProofProtectsSubmitRetryAndAcceptedReceipt(t *testing.T) {
	for _, mutation := range []string{"forget", "revise", "revoke"} {
		for _, phase := range []string{"new", "retry", "accepted"} {
			t.Run(mutation+"/"+phase, func(t *testing.T) {
				w, e, in, _ := privateProofFixture(t)
				e.Config.Permissions.SendVoice = true
				n := &mediaFake{keys: map[string]bool{}}
				w.sender = n
				w.config.Draining = func() bool { return true }
				w.queueMedia(context.Background(), e, in, companion.GeneratedMedia{ID: "synthetic-audio", Kind: "voice", Title: "synthetic.wav"})
				d := w.runtime.Media[0]
				if d.Input.ReplyContext == nil || d.State != "prepared" {
					t.Fatal("queued media lacks proof", d.Reason)
				}
				if phase != "new" {
					n.status = linuxnative.ToolStatus{ID: "existing-job", ThreadID: in.Thread, SourceID: d.SourceID, State: "failed", Retryable: true}
					d.State, d.Checks, d.ID = "retry_wait", 1, n.status.ID
					if phase == "accepted" {
						// Crash left only the idempotency key in the bot checkpoint.
						d.State, d.ID = "submitting", ""
						n.keys[d.Key] = true
						n.status.State, n.status.ServerID = "accepted", "verified-native-receipt"
					}
				}
				changePrivateEvidence(t, e, in, mutation)
				w.config.Draining = nil
				w.reconcileMedia(context.Background())
				if n.sends != 0 || n.retries != 0 {
					t.Fatal("stale media submitted/retried", n.sends, n.retries)
				}
				if phase == "accepted" {
					if d.State != "accepted" || d.Status.ServerID == "" {
						t.Fatal("accepted receipt discarded", d.State, d.Reason)
					}
				} else if d.State != "needs_review" {
					t.Fatal("stale media not blocked", d.State, d.Reason)
				}
			})
		}
	}
}

func TestCrossMediaUsesOriginProofAndPreservesAcceptedReceipt(t *testing.T) {
	for _, mutation := range []string{"forget", "revise", "revoke"} {
		for _, accepted := range []bool{false, true} {
			t.Run(mutation+map[bool]string{false: "/unsent", true: "/accepted"}[accepted], func(t *testing.T) {
				w, e, origin, _ := privateProofFixture(t)
				n := &mediaFake{keys: map[string]bool{}}
				w.sender = n
				target := origin
				target.Thread = "target-without-auto-reply@chatroom"
				seed := MediaSeed{OriginInput: &origin, Key: "synthetic-cross-key", Trace: origin.TraceID, Input: target, Requester: origin.Sender, OriginThread: origin.Thread, SourceID: "synthetic-cross-source", Media: companion.GeneratedMedia{Kind: "voice"}}
				w.config.MediaSeeds = func() []MediaSeed { return []MediaSeed{seed} }
				w.config.MediaSeedAllowed = func(MediaSeed) bool { return true }
				w.adoptMediaSeeds()
				if err := w.loadRuntime("server-1"); err != nil {
					t.Fatal(err)
				}
				d := w.runtime.Media[0]
				if d.OriginInput == nil || d.OriginInput.Thread != origin.Thread {
					t.Fatal("origin proof was replaced by route")
				}
				if accepted {
					n.keys[d.Key] = true
					n.status = linuxnative.ToolStatus{ID: "known-job", ThreadID: target.Thread, SourceID: d.SourceID, State: "accepted", ServerID: "verified-cross-receipt"}
				}
				changePrivateEvidence(t, e, origin, mutation)
				w.reconcileMedia(context.Background())
				if n.sends != 0 || n.retries != 0 {
					t.Fatal("stale cross-origin submitted", n.sends, n.retries)
				}
				want := "needs_review"
				if accepted {
					want = "accepted"
				}
				if d.State != want {
					t.Fatal("cross proof/receipt lost", d.State, d.Reason, want)
				}
			})
		}
	}
}

func TestLegacyMediaCannotAcquireFreshProofAtSubmit(t *testing.T) {
	w, e, in, _ := privateProofFixture(t)
	n := &mediaFake{keys: map[string]bool{}}
	w.sender = n
	w.config.Draining = func() bool { return true }
	w.queueMedia(context.Background(), e, in, companion.GeneratedMedia{ID: "synthetic-legacy", Kind: "voice"})
	d := w.runtime.Media[0]
	d.Input.ReplyContext = nil // old checkpoint predates proof persistence
	if err := w.saveRuntime(); err != nil {
		t.Fatal(err)
	}
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	w.config.Draining = nil
	w.reconcileMedia(context.Background())
	if n.sends != 0 || w.runtime.Media[0].State != "needs_review" {
		t.Fatal("legacy checkpoint was stamped at dispatch", n.sends, w.runtime.Media[0].State)
	}
}

type evidenceReconcileNative struct {
	mediaFake
	reconciles int
}

func (n *evidenceReconcileNative) ReconcileMediaJob(context.Context, string) (linuxnative.ToolStatus, error) {
	n.reconciles++
	return n.status, nil
}

func TestExpiredMediaProofOnlyPollsUncertainReceipt(t *testing.T) {
	w, e, in, _ := privateProofFixture(t)
	n := &evidenceReconcileNative{mediaFake: mediaFake{keys: map[string]bool{}}}
	w.sender = n
	w.config.Draining = func() bool { return true }
	w.queueMedia(context.Background(), e, in, companion.GeneratedMedia{ID: "synthetic-uncertain", Kind: "voice"})
	d := w.runtime.Media[0]
	d.ID, d.State, d.Checks = "original-uncertain-job", "waiting", 1
	n.status = linuxnative.ToolStatus{ID: d.ID, ThreadID: in.Thread, SourceID: d.SourceID, State: "unknown"}
	d.Status = n.status
	changePrivateEvidence(t, e, in, "forget")
	w.config.Draining = nil
	w.reconcileMedia(context.Background())
	if n.reconciles != 0 || n.sends != 0 || n.retries != 0 || d.State != "waiting" {
		t.Fatal("expired uncertain job resumed", n.reconciles, d.State)
	}
	n.status.State, n.status.ServerID = "accepted", "late-verified-receipt"
	d.Next = time.Time{}
	w.reconcileMedia(context.Background())
	if d.State != "accepted" || n.reconciles != 0 {
		t.Fatal("late receipt discarded or resent", d.State, n.reconciles)
	}
}

func TestCanonicalCrossSeedCanReconcileAfterOriginRevocation(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		w, e, origin, _ := privateProofFixture(t)
		n := &mediaFake{keys: map[string]bool{}}
		w.sender = n
		target := origin
		target.Thread = "authorized-canonical-target@chatroom"
		seed := MediaSeed{OriginInput: &origin, Key: "canonical-key", Trace: "canonical-batch", Input: target, Requester: origin.Sender, OriginThread: origin.Thread, SourceID: "canonical-source", Media: companion.GeneratedMedia{Kind: "voice"}}
		w.config.MediaSeeds = func() []MediaSeed { return []MediaSeed{seed} }
		w.config.MediaSeedKnown = func(s MediaSeed) bool {
			return s.Key == seed.Key && s.Trace == seed.Trace && s.Input.Thread == target.Thread
		}
		w.config.MediaSeedAllowed = func(MediaSeed) bool { return false }
		changePrivateEvidence(t, e, origin, "forget")
		if accepted {
			n.keys[seed.Key] = true
			n.status = linuxnative.ToolStatus{ID: "canonical-job", ThreadID: target.Thread, SourceID: seed.SourceID, State: "accepted", ServerID: "canonical-receipt"}
		}
		w.adoptMediaSeeds()
		if len(w.runtime.Media) != 1 {
			t.Fatal("known canonical key was discarded before receipt lookup")
		}
		w.reconcileMedia(context.Background())
		want := "stopped"
		if accepted {
			want = "accepted"
		}
		if d := w.runtime.Media[0]; d.State != want || n.sends != 0 || n.retries != 0 {
			t.Fatal("read-only and current send authorization mixed", d.State, want, n.sends, n.retries)
		}
	}
}

func TestQuietHoursAllowOnlyVerifiedProtectedAdminAction(t *testing.T) {
	w, _, e, original := mediaWorker(t)
	e.Config.Permissions.Remember = false
	e.Config.QuietStart = time.Now().Add(-time.Hour).In(time.FixedZone("local-test", 8*3600)).Format("15:04")
	e.Config.QuietEnd = time.Now().Add(time.Hour).In(time.FixedZone("local-test", 8*3600)).Format("15:04")
	if !e.Config.Quiet(time.Now()) {
		t.Fatal("synthetic quiet interval not active")
	}
	origin := companion.Input{Channel: original.Channel, Account: original.Account, Thread: original.Thread, Sender: "trusted-admin", Mode: "admin_action", TraceID: "protected-click", OriginTraceID: "protected-click"}
	r, err := e.BindReplyEvidence(context.Background(), origin)
	if err != nil {
		t.Fatal(err)
	}
	origin.ReplyContext = &r
	target := origin
	target.Thread = "explicit-admin-target@chatroom"
	d := &mediaDelivery{Input: target, OriginInput: &origin, CrossRequester: origin.Sender, Kind: "voice", Key: "canonical-gui-key"}
	w.config.MediaSeedAllowed = func(MediaSeed) bool { return true }
	if !w.mediaAllowed(d) {
		t.Fatal("verified explicit admin action was blocked as autonomous chat")
	}
	bad := r
	bad.ContextVersion = "forged"
	origin.ReplyContext = &bad
	if w.mediaAllowed(d) {
		t.Fatal("mode string bypassed missing valid origin proof")
	}
	origin.ReplyContext = nil
	if w.mediaAllowed(d) {
		t.Fatal("unproven admin mode bypassed quiet hours")
	}
	d.OriginInput = nil
	d.Input.Mode = "social"
	if w.mediaAllowed(d) {
		t.Fatal("ordinary autonomous delivery bypassed quiet hours")
	}
}
