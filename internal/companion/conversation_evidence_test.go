package companion

import (
	"context"
	"encoding/json"
	"testing"
	"time"
	"wechat-bot/internal/mcptools"
)

type evidenceReviewAccess struct {
	state *evidenceToolAccess
	trace Input
}

func (a *evidenceReviewAccess) List(ctx context.Context) ([]mcptools.Tool, error) {
	return a.state.List(ctx)
}
func (a *evidenceReviewAccess) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	return a.state.Call(ctx, name, args)
}
func (a *evidenceReviewAccess) SetTrace(in Input) { a.trace = in }
func (a *evidenceReviewAccess) ForkTools() ToolAccess {
	return &evidenceReviewAccess{state: a.state, trace: a.trace}
}
func (a *evidenceReviewAccess) MemoryResultAuthorization() string {
	return a.state.MemoryResultAuthorization() + "|" + a.trace.Account + "|" + a.trace.Thread + "|" + a.trace.Sender
}
func (a *evidenceReviewAccess) MemoryResultAuthorized(stamp string) bool {
	return stamp == a.MemoryResultAuthorization()
}

func pendingEvidenceFixture(t *testing.T, model LanguageModel) (*Store, *Engine, Input, Input, *evidenceReviewAccess) {
	t.Helper()
	s, c, in, background := generationFixture(t)
	if err := s.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	ref, ok := s.SourceForInput(in)
	if !ok {
		t.Fatal("original request missing")
	}
	in.Source = ref
	e, err := New(c, s, model, "synthetic policy")
	if err != nil {
		t.Fatal(err)
	}
	base := &evidenceReviewAccess{state: newEvidenceToolAccess(), trace: Input{Account: "unrelated-account", Thread: "unrelated-room", Sender: "unrelated-person"}}
	e.Tools = base
	prepared, err := e.Fork()
	if err != nil {
		t.Fatal(err)
	}
	prepared.Tools.(interface{ SetTrace(Input) }).SetTrace(in)
	proof, err := prepared.BindReplyEvidence(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.ReplyContext = &proof
	return s, e, in, background, base
}

func callPendingEvidenceReview(ctx context.Context, e *Engine, kind string, in Input) (PendingReview, error) {
	switch kind {
	case "generation":
		return e.ReviewGeneration(ctx, in, []string{"original authorized task"}, nil)
	case "caption":
		return e.ReviewMediaCaption(ctx, in, nil, []string{"original caption"}, nil, []DeliveredAttachment{{Kind: "video", State: "accepted"}})
	case "delivery":
		return e.ReviewDelivery(ctx, in, nil, "original pending text", DeliveryReview{Reason: "outcome_unknown"})
	default:
		return e.ReviewPending(ctx, in, nil, []string{"original pending text"}, nil)
	}
}

func TestPendingReviewRejectsInFlightEvidenceChanges(t *testing.T) {
	for _, kind := range []string{"pending", "generation", "caption", "delivery"} {
		for _, mutation := range []string{"forget", "revoke"} {
			t.Run(kind+"/"+mutation, func(t *testing.T) {
				m := &evidenceBlockingModel{entered: make(chan GenerationEvidence, 1), release: make(chan struct{}), answer: `{"action":"continue","messages":[]}`}
				s, e, in, background, base := pendingEvidenceFixture(t, m)
				before, _ := json.Marshal(in.ReplyContext)
				baseTrace := base.trace
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				finished := make(chan error, 1)
				go func() {
					_, err := callPendingEvidenceReview(ctx, e, kind, in)
					finished <- err
				}()
				select {
				case stamp := <-m.entered:
					if stamp.RecallEpoch != in.ReplyContext.EvidenceEpoch || stamp.Authorization != in.ReplyContext.EvidenceAuthorization {
						t.Fatal("review did not inherit plan's original evidence", stamp, in.ReplyContext)
					}
				case err := <-finished:
					t.Fatal("review stopped before blocked model", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if base.trace.Account != baseTrace.Account || base.trace.Thread != baseTrace.Thread || base.trace.Sender != baseTrace.Sender {
					t.Fatal("review mutated base/shared trace", base.trace)
				}
				if mutation == "forget" {
					if err := s.Clear(ScopeID(background.Channel, background.Account, background.Thread)); err != nil {
						t.Fatal(err)
					}
				} else {
					base.state.authorization.Store("revoked-review-authorization")
				}
				close(m.release)
				select {
				case err := <-finished:
					if err == nil || err.Error() != "generation_evidence_changed" {
						t.Fatal("stale review result escaped", err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				after, _ := json.Marshal(in.ReplyContext)
				if string(before) != string(after) || base.state.calls.Load() != 0 {
					t.Fatal("review rewrote old evidence or executed a tool", string(after), base.state.calls.Load())
				}
			})
		}
	}
}

func TestPendingReviewReturnsOnlyHostOriginalEvidence(t *testing.T) {
	m := &fakeModel{answer: `{"action":"replace","messages":["revised text"],"generationEvidence":{"RecallEpoch":"forged"}}`}
	_, e, in, _, base := pendingEvidenceFixture(t, m)
	r, err := e.ReviewPending(t.Context(), in, nil, []string{"old text"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stamp, ok := r.GenerationEvidence()
	if !ok || stamp.RecallEpoch != in.ReplyContext.EvidenceEpoch || stamp.Authorization != in.ReplyContext.EvidenceAuthorization || m.calls != 1 {
		t.Fatal("review did not return immutable host evidence", r, stamp, m.calls)
	}
	ctx := WithPendingReviewGenerationEvidence(t.Context(), r)
	if actual, ok := GenerationEvidenceFromContext(ctx); !ok || actual != stamp {
		t.Fatal("review-to-plan context lost original evidence", actual, ok)
	}
	if _, ok := GenerationEvidenceFromContext(WithPendingReviewGenerationEvidence(t.Context(), PendingReview{})); ok {
		t.Fatal("legacy review acquired a fabricated stamp")
	}
	base.state.authorization.Store("revoked-after-review")
	if _, err = e.ReviewPending(t.Context(), in, nil, []string{"old text"}, nil); err == nil || m.calls != 1 {
		t.Fatal("old plan was re-signed or sent to model after revocation", err, m.calls)
	}
}

func TestPendingReviewRejectsMutableToolsWithoutIsolatedTrace(t *testing.T) {
	s, c, in, _ := generationFixture(t)
	m := &fakeModel{answer: `{"action":"continue"}`}
	e, err := New(c, s, m, "synthetic policy")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools = newEvidenceToolAccess() // SetTrace exists, ForkTools deliberately does not.
	if _, err = e.ReviewPending(t.Context(), in, nil, []string{"old"}, nil); err == nil || m.calls != 0 {
		t.Fatal("shared mutable trace was used for a plan review", err, m.calls)
	}
}

func TestPendingReviewWireSeparatesLiteralMediaAndQuoteRoles(t *testing.T) {
	s, c, in, background := generationFixture(t)
	if err := s.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	in.Source, _ = s.SourceForInput(in)
	caption := "这是我转发的资料"
	in.TraceID = "quoted-image"
	in.Source = &SourceRef{InstanceID: s.SourceInstanceID(), MessageID: in.TraceID, ServerID: "image-server", Sender: in.Sender, MessageType: 3, Revision: 1, OccurredAt: in.MessageTime}
	in.OriginalText = &caption
	in.Text = caption + "\n机器观察：照片中有一把雨伞"
	in.QuotedSource = background.Source
	in.IncomingMedia = []IncomingMediaRef{{Source: *in.Source, Kind: "image", UploaderID: in.Sender, Observation: "照片中有一把雨伞", ObservationState: "observed", ObservationVersion: 1, ObservedRange: "single image"}}
	if err := s.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	in.Source, _ = s.SourceForInput(in)
	m := &fakeModel{answer: `{"action":"continue","messages":[]}`}
	e, err := New(c, s, m, "synthetic policy")
	if err != nil {
		t.Fatal(err)
	}
	update := ConversationEvent{MessageID: "unpersisted-quote", Sender: "quoter", Text: "<msg>not literal words</msg>", ReplyTo: &SourceRef{MessageID: "original-message", Sender: "original-author"}, Source: &SourceRef{Account: in.Account, Thread: in.Thread, Sender: "quoter", MessageID: "unpersisted-quote", MessageType: 49}}
	if _, err = e.ReviewPending(t.Context(), in, nil, []string{"old caption"}, []ConversationEvent{update}); err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Request map[string]any   `json:"original_request"`
		Events  []map[string]any `json:"new_events"`
	}
	if err = json.Unmarshal([]byte(m.turns[0]["content"]), &wire); err != nil {
		t.Fatal(err)
	}
	observations, _ := wire.Request["media_observations"].([]any)
	if wire.Request["text"] != caption || len(observations) != 1 {
		t.Fatal("machine observation became literal request", wire.Request)
	}
	observation, _ := observations[0].(map[string]any)
	if observation["producer"] != "media_observer" || observation["uploader_id"] != in.Sender || observation["observation"] != "照片中有一把雨伞" || observation["owner_id"] != nil {
		t.Fatal("media identity roles changed", observation)
	}
	if len(wire.Events) != 1 || wire.Events[0]["text"] != "" || wire.Events[0]["quoted_by_id"] != "quoter" || wire.Events[0]["quoted_author_id"] != "original-author" {
		t.Fatal("quote author and quoting member were merged or XML became speech", wire.Events)
	}
}

func TestPendingReviewNativeFallbackDoesNotRestoreForgottenOrRevisedText(t *testing.T) {
	for _, mutation := range []string{"unpersisted", "forgotten", "revised"} {
		t.Run(mutation, func(t *testing.T) {
			s, c, in, _ := generationFixture(t)
			e, err := New(c, s, &fakeModel{}, "synthetic policy")
			if err != nil {
				t.Fatal(err)
			}
			update := in
			update.TraceID, update.Text = "pending-stop", "请停止之前的任务"
			update.Source = &SourceRef{InstanceID: s.SourceInstanceID(), Account: in.Account, Thread: in.Thread, Sender: in.Sender, MessageID: update.TraceID, ServerID: "stop-server", OccurredAt: update.MessageTime, MessageType: 1, BodyHash: sourceHash(update.Text), Revision: 1}
			update.OriginalText = &update.Text
			event := ConversationEvent{MessageID: update.TraceID, Sender: update.Sender, Text: update.Text, Source: cloneSource(update.Source), Relation: "same_sender_followup"}
			if mutation != "unpersisted" {
				if err = s.Observe(update, c, time.Now()); err != nil {
					t.Fatal(err)
				}
				if mutation == "forgotten" {
					err = s.Clear(ScopeID(in.Channel, in.Account, in.Thread))
				} else {
					update.Source.Revision++
					update.Text = "更正：继续之前的任务"
					err = s.Observe(update, c, time.Now())
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			rows := e.pendingEventEvidence(t.Context(), in, []ConversationEvent{event})
			if len(rows) != 1 {
				t.Fatal(rows)
			}
			if mutation == "unpersisted" {
				if rows[0]["text"] != event.Text || rows[0]["speaker_id"] != in.Sender || rows[0]["user_text_state"] != "native_pending_original" {
					t.Fatal("new native cancellation lost its literal or author", rows)
				}
			} else if rows[0]["text"] != "" || rows[0]["user_text_state"] != "invalidated" {
				t.Fatal("forgotten or stale native preview re-entered as current human wording", rows)
			}
		})
	}
}
