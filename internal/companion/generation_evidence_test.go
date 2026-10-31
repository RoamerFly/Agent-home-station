package companion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"wechat-bot/internal/mcptools"
)

type evidenceBlockingModel struct {
	entered chan GenerationEvidence
	release chan struct{}
	answer  string
}

func (m *evidenceBlockingModel) Chat(ctx context.Context, _ string, _ []map[string]string) (string, error) {
	evidence, _ := GenerationEvidenceFromContext(ctx)
	select {
	case m.entered <- evidence:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	select {
	case <-m.release:
		return m.answer, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type evidenceToolAccess struct {
	authorization atomic.Value
	calls         atomic.Int32
	beforeCall    func(context.Context, string) error
	trace         Input
	reset         func()
}

func (a *evidenceToolAccess) SetTrace(in Input) { a.trace = in }
func (a *evidenceToolAccess) ResetMedia() {
	if a.reset != nil {
		a.reset()
	}
}

func newEvidenceToolAccess() *evidenceToolAccess {
	a := &evidenceToolAccess{}
	a.authorization.Store("authorized-current-and-background")
	return a
}

func (a *evidenceToolAccess) MemoryResultAuthorization() string {
	return a.authorization.Load().(string)
}

func (a *evidenceToolAccess) MemoryResultAuthorized(stamp string) bool {
	return stamp != "" && stamp == a.MemoryResultAuthorization()
}

func (*evidenceToolAccess) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{
		{Name: "memory_update", Description: "update a sourced memory", InputSchema: json.RawMessage(`{}`)},
		{Name: "wechat_send_text", Description: "send authorized text", InputSchema: json.RawMessage(`{}`)},
	}, nil
}

func (a *evidenceToolAccess) Call(ctx context.Context, name string, _ json.RawMessage) (string, error) {
	a.calls.Add(1)
	if a.beforeCall != nil {
		if err := a.beforeCall(ctx, name); err != nil {
			return "", err
		}
	}
	return `{"status":"succeeded"}`, nil
}

func generationFixture(t *testing.T) (*Store, Config, Input, Input) {
	t.Helper()
	s, c, in := memoryFixture(t)
	c.Enabled = true
	c.QuietStart, c.QuietEnd = "00:00", "00:00"
	in.Mode, in.Text = "direct", "请根据相关资料回复"
	in.MessageTime = time.Now().UTC().Format(time.RFC3339Nano)
	in.Source = &SourceRef{InstanceID: s.SourceInstanceID(), MessageID: in.TraceID, ServerID: "server-request", OccurredAt: in.MessageTime, Revision: 1}
	in.OriginalText = &in.Text
	background := in
	background.Thread, background.TraceID, background.Text = "background-room", "background-source", "这是另一条会话中的相关资料"
	background.Source = &SourceRef{InstanceID: s.SourceInstanceID(), MessageID: background.TraceID, ServerID: "server-background", OccurredAt: background.MessageTime, Revision: 1}
	background.OriginalText = &background.Text
	if err := s.Observe(background, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	return s, c, in, background
}

func TestGenerationEvidenceRejectsInFlightResults(t *testing.T) {
	for _, action := range []string{"forget_reply", "forget_tool", "revoke_reply", "revoke_tool", "revise_reply"} {
		t.Run(action, func(t *testing.T) {
			s, c, in, background := generationFixture(t)
			answer := `{"action":"reply","messages":["这条回复使用了之前的资料"]}`
			if strings.HasSuffix(action, "_tool") {
				answer = `{"action":"tool","tool":"wechat_send_text","arguments":{"text":"之前的资料"}}`
			}
			m := &evidenceBlockingModel{entered: make(chan GenerationEvidence, 1), release: make(chan struct{}), answer: answer}
			e, err := New(c, s, m, "synthetic policy")
			if err != nil {
				t.Fatal(err)
			}
			access := newEvidenceToolAccess()
			e.Tools, e.MaxToolCalls = access, 4
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			finished := make(chan error, 1)
			go func() {
				_, err := e.Respond(ctx, in)
				finished <- err
			}()
			select {
			case stamp := <-m.entered:
				if stamp.Authorization != access.MemoryResultAuthorization() || stamp.RecallEpoch == "" {
					t.Fatal("model did not receive actual original evidence", stamp)
				}
			case err := <-finished:
				t.Fatal("generation stopped before the blocked model", err)
			case <-ctx.Done():
				t.Fatal("model did not start", ctx.Err())
			}
			switch {
			case strings.HasPrefix(action, "forget"):
				err = s.Clear(ScopeID(background.Channel, background.Account, background.Thread))
			case strings.HasPrefix(action, "revoke"):
				access.authorization.Store("authorized-current-only")
			case strings.HasPrefix(action, "revise"):
				background.Text = "更正后的资料"
				background.Source.Revision++
				err = s.Observe(background, c, time.Now())
			}
			if err != nil {
				t.Fatal(err)
			}
			// The request itself remains present; the invalidation concerns other
			// evidence in the generated decision, not merely a missing anchor.
			if _, ok := s.SourceForInput(in); !ok {
				t.Fatal("current request unexpectedly removed")
			}
			close(m.release)
			select {
			case err = <-finished:
				if err == nil || err.Error() != "generation_evidence_changed" {
					t.Fatal("stale result was re-signed or executed", err)
				}
			case <-ctx.Done():
				t.Fatal("generation did not stop", ctx.Err())
			}
			if access.calls.Load() != 0 {
				t.Fatal("stale generated tool executed", access.calls.Load())
			}
		})
	}
}

func TestGenerationEvidenceIndependentAppendRemainsValid(t *testing.T) {
	s, c, in, background := generationFixture(t)
	m := &evidenceBlockingModel{entered: make(chan GenerationEvidence, 1), release: make(chan struct{}), answer: `{"action":"reply","messages":["收到"]}`}
	e, err := New(c, s, m, "synthetic policy")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools = newEvidenceToolAccess()
	type result struct {
		d   Decision
		err error
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	finished := make(chan result, 1)
	go func() {
		d, err := e.Respond(ctx, in)
		finished <- result{d, err}
	}()
	var original GenerationEvidence
	select {
	case original = <-m.entered:
	case result := <-finished:
		t.Fatal("generation stopped before model", result.err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	background.TraceID = "independent-append"
	background.Source = &SourceRef{InstanceID: s.SourceInstanceID(), MessageID: background.TraceID, ServerID: "another-server", OccurredAt: background.MessageTime, Revision: 1}
	background.Text = "无关的新消息"
	if err = s.Observe(background, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	close(m.release)
	select {
	case result := <-finished:
		if result.err != nil || result.d.Action != "reply" || result.d.generationEvidence == nil || *result.d.generationEvidence != original {
			t.Fatal("ordinary append invalidated or replaced the original stamp", result.d, result.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

type evidenceBatchModel struct{ calls int }

func (*evidenceBatchModel) Chat(context.Context, string, []map[string]string) (string, error) {
	return `{"action":"reply","messages":["收到"]}`, nil
}

func (m *evidenceBatchModel) ChatFunctions(context.Context, string, []map[string]string, []mcptools.Tool) (string, error) {
	m.calls++
	return `{"action":"tool_batch","calls":[{"action":"tool","tool":"memory_update","arguments":{}},{"action":"tool","tool":"wechat_send_text","arguments":{"text":"旧资料"}}]}`, nil
}

func TestGenerationEvidenceInvalidatesPendingNativeBatch(t *testing.T) {
	s, c, in, background := generationFixture(t)
	m := &evidenceBatchModel{}
	e, err := New(c, s, m, "synthetic policy")
	if err != nil {
		t.Fatal(err)
	}
	access := newEvidenceToolAccess()
	access.beforeCall = func(ctx context.Context, name string) error {
		stamp, ok := GenerationEvidenceFromContext(ctx)
		if !ok || stamp.Authorization != access.MemoryResultAuthorization() {
			t.Fatal("tool adapter did not receive original model stamp", stamp, ok)
		}
		if name != "memory_update" {
			t.Fatal("stale sibling send executed", name)
		}
		return s.Clear(ScopeID(background.Channel, background.Account, background.Thread))
	}
	e.Tools, e.MaxToolCalls = access, 4
	_, err = e.Respond(t.Context(), in)
	if err == nil || err.Error() != "generation_evidence_changed" || access.calls.Load() != 1 || m.calls != 1 {
		t.Fatal("native batch reused old generated arguments", err, access.calls.Load(), m.calls)
	}
	var partial *GenerationEvidenceChangedError
	if !errors.As(err, &partial) || partial.Stage != "generation_paused" || len(partial.CompletedTools) != 1 || partial.CompletedTools[0] != "memory_update" || partial.Continuation == "" {
		t.Fatal("completed tool was lost or reported as failed", err)
	}
}

func TestGenerationEvidenceDecisionStampCannotBeProvidedByModelJSON(t *testing.T) {
	var d Decision
	if err := json.Unmarshal([]byte(`{"action":"reply","messages":["hello"],"generationEvidence":{"RecallEpoch":"999","Authorization":"forged"}}`), &d); err != nil {
		t.Fatal(err)
	}
	if d.generationEvidence != nil {
		t.Fatal("model forged a host generation stamp")
	}
}

func TestGenerationEvidencePostDecisionContextDoesNotRefreshStamp(t *testing.T) {
	s, c, _, background := generationFixture(t)
	e, err := New(c, s, &fakeModel{}, "synthetic policy")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools = newEvidenceToolAccess()
	original, err := e.captureGenerationEvidence(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	d := Decision{Action: "reply", Messages: []string{"old generated reply"}, generationEvidence: &original}
	if err = s.Clear(ScopeID(background.Channel, background.Account, background.Thread)); err != nil {
		t.Fatal(err)
	}
	ctx := WithDecisionGenerationEvidence(t.Context(), d)
	stamp, ok := GenerationEvidenceFromContext(ctx)
	if !ok || stamp != original {
		t.Fatal("post-decision preparation replaced old stamp", stamp, original)
	}
	if err = e.ValidateGenerationEvidence(ctx, stamp); err == nil || err.Error() != "generation_evidence_changed" {
		t.Fatal("old generation became current again", err)
	}
	copy, _ := d.GenerationEvidence()
	copy.Authorization = "caller changed its copy"
	if actual, _ := d.GenerationEvidence(); actual != original {
		t.Fatal("read-only accessor mutated host proof", actual)
	}
	if _, ok := GenerationEvidenceFromContext(WithDecisionGenerationEvidence(t.Context(), Decision{})); ok {
		t.Fatal("legacy decision acquired a fabricated stamp")
	}
}

func TestGenerationEvidenceFinalAndLegacyCompletionRejectInFlightChange(t *testing.T) {
	for _, path := range []string{"final_response", "complete"} {
		t.Run(path, func(t *testing.T) {
			s, c, _, background := generationFixture(t)
			m := &evidenceBlockingModel{entered: make(chan GenerationEvidence, 1), release: make(chan struct{}), answer: `{"action":"reply","messages":["old context"]}`}
			e, err := New(c, s, m, "synthetic policy")
			if err != nil {
				t.Fatal(err)
			}
			e.Tools = newEvidenceToolAccess()
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			finished := make(chan error, 1)
			go func() {
				if path == "complete" {
					_, err := e.Complete(ctx, "a synthetic request")
					finished <- err
					return
				}
				runner := &AgentRunner{Engine: e}
				_, err := runner.finalResponse(ctx, "synthetic policy", nil, "synthetic_feedback", "")
				finished <- err
			}()
			select {
			case <-m.entered:
			case err := <-finished:
				t.Fatal("completion stopped before model", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err = s.Clear(ScopeID(background.Channel, background.Account, background.Thread)); err != nil {
				t.Fatal(err)
			}
			close(m.release)
			select {
			case err := <-finished:
				if err == nil || err.Error() != "generation_evidence_changed" {
					t.Fatal("stale completion escaped", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}

func TestGenerationEvidenceSourceCannotUpgradeBeforeAssemblyCapture(t *testing.T) {
	s, c, in, _ := generationFixture(t)
	m := &fakeModel{answer: `{"action":"reply","messages":["收到"]}`}
	e, err := New(c, s, m, "synthetic policy")
	if err != nil {
		t.Fatal(err)
	}
	access := newEvidenceToolAccess()
	access.reset = func() {
		// ResetMedia runs after the first SourceForInput resolution, but before
		// model-packet assembly. Reproduce the old lookup/capture race exactly.
		revised := access.trace
		revised.Source = cloneSource(revised.Source)
		revised.Source.Revision++
		revised.Text = "更正：不要执行之前那条要求"
		revised.OriginalText = &revised.Text
		if err := s.Observe(revised, c, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	e.Tools = access
	_, err = e.Respond(t.Context(), in)
	if err == nil || err.Error() != "generation_evidence_changed" || m.calls != 0 || access.calls.Load() != 0 {
		t.Fatal("old request was signed against a new pre-assembly epoch", err, m.calls, access.calls.Load())
	}
}

func TestGenerationEvidenceResolvedSourceRequiresOriginalVersionAndBody(t *testing.T) {
	for _, shape := range []string{"stale_plan", "wrong_literal_body", "verified_new_revision"} {
		t.Run(shape, func(t *testing.T) {
			s, c, in, _ := generationFixture(t)
			if err := s.Observe(in, c, time.Now()); err != nil {
				t.Fatal(err)
			}
			ref, ok := s.SourceForInput(in)
			if !ok {
				t.Fatal("initial source missing")
			}
			in.Source = ref
			in.HistoryObserved = true
			switch shape {
			case "stale_plan":
				revised := in
				revised.Source = cloneSource(ref)
				revised.Source.Revision++
				revised.Text = "更正后的请求"
				revised.OriginalText = &revised.Text
				if err := s.Observe(revised, c, time.Now()); err != nil {
					t.Fatal(err)
				}
			case "wrong_literal_body":
				in.Text = "不同正文，不能借用现有消息身份"
				in.OriginalText = &in.Text
			case "verified_new_revision":
				// The adapter explicitly supplies a new revision. Observe commits
				// its actual literal body, even if its reused ref had the old hash.
				in.HistoryObserved = false
				in.Source.Revision++
				in.Text = "已核验的正文新版本"
				in.OriginalText = &in.Text
			}
			m := &fakeModel{answer: `{"action":"reply","messages":["收到"]}`}
			e, err := New(c, s, m, "synthetic policy")
			if err != nil {
				t.Fatal(err)
			}
			access := newEvidenceToolAccess()
			e.Tools = access
			d, err := e.Respond(t.Context(), in)
			if shape == "verified_new_revision" {
				if err != nil || d.Action != "reply" || m.calls != 1 {
					t.Fatal("genuine inbound revision was rejected", d, err, m.calls)
				}
				if access.trace.Source == nil || access.trace.Source.Revision != in.Source.Revision || access.trace.Source.BodyHash != sourceHash(*in.OriginalText) || in.Source.BodyHash == access.trace.Source.BodyHash {
					t.Fatal("new revision did not use actual stored canonical source or mutated caller proof", access.trace.Source, in.Source)
				}
				return
			}
			if err == nil || (err.Error() != "generation_evidence_changed" && err.Error() != "source_identity_conflict_or_unavailable") || m.calls != 0 {
				t.Fatal("old or mismatched body acquired a newer identity", d, err, m.calls)
			}
		})
	}
}

func TestGenerationEvidenceCanonicalHashRepairIsNotAvailableToQueuedProofs(t *testing.T) {
	for _, shape := range []string{"history_observed", "queued_runtime", "persisted_reply_proof", "existing_generation_proof", "fresh_wrong_base_hash"} {
		t.Run(shape, func(t *testing.T) {
			s, c, in, _ := generationFixture(t)
			if err := s.Observe(in, c, time.Now()); err != nil {
				t.Fatal(err)
			}
			ref, ok := s.SourceForInput(in)
			if !ok {
				t.Fatal("initial source missing")
			}
			in.Source = cloneSource(ref)
			in.Source.Revision++
			in.Text = "新的完整原生正文版本"
			in.OriginalText = &in.Text
			ctx := t.Context()
			switch shape {
			case "history_observed", "queued_runtime":
				// Commit v2 independently, then present a queued v2 reference that
				// still carries the old v1 hash. Neither mode may obtain v2 proof.
				if err := s.Observe(in, c, time.Now()); err != nil {
					t.Fatal(err)
				}
				if shape == "history_observed" {
					in.HistoryObserved = true
				} else {
					in.TaskID, in.BackgroundResult, in.TraceID = "queued-task", true, "task-run"
				}
			case "persisted_reply_proof":
				in.ReplyContext = &ReplyContext{EvidenceEpoch: "previous-plan-proof", SourceVersions: map[string]int{ref.EventID: ref.Revision}}
			case "existing_generation_proof":
				ctx = WithStoredGenerationEvidence(ctx, GenerationEvidence{RecallEpoch: "previous-generation-proof"})
			case "fresh_wrong_base_hash":
				in.Source.BodyHash = sourceHash("not the committed revision base")
			}
			m := &fakeModel{answer: `{"action":"reply","messages":["不应执行"]}`}
			e, err := New(c, s, m, "synthetic policy")
			if err != nil {
				t.Fatal(err)
			}
			access := newEvidenceToolAccess()
			e.Tools = access
			d, err := e.Respond(ctx, in)
			if err == nil || (err.Error() != "source_identity_conflict_or_unavailable" && err.Error() != "generation_evidence_changed") || m.calls != 0 || access.calls.Load() != 0 {
				t.Fatal("queued or invalid canonical hash acquired fresh generation proof", shape, d, err, m.calls, access.calls.Load())
			}
		})
	}
}
