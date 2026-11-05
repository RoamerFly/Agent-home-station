package companion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestGroundingVerdictStrictSchema(t *testing.T) {
	messages := []string{"safe message"}
	good := atomicTestWire(groundingReviewUnits(messages), atomicNonfactual)
	if _, err := decodeGroundingVerdict(good, "bound", messages, []string{"source"}); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"unexpected_model_id":   strings.Replace(good, `"units":`, `"candidate_id":"untrusted","units":`, 1),
		"rewriting":             strings.Replace(good, `"units":`, `"messages":["new words"],"units":`, 1),
		"missing_unit":          `{"units":[]}`,
		"legacy_allow_shortcut": `{"bubbles":[{"index":0,"verdict":"allow","violations":[]}]}`,
		"unknown_status":        strings.Replace(good, `"status":"nonfactual"`, `"status":"maybe"`, 1),
		"null_supports":         strings.Replace(good, `"supports":[]`, `"supports":null`, 1),
		"invented_span":         strings.Replace(good, `"span":"safe message"`, `"span":"invented"`, 1),
		"unexpected_overall":    strings.Replace(good, `"units":`, `"verdict":"allow","units":`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeGroundingVerdict(raw, "bound", messages, []string{"source"}); err == nil {
				t.Fatal("invalid verdict accepted", raw)
			}
		})
	}
}

func TestGroundingCheckDoesNotEditAllowedCandidate(t *testing.T) {
	s, c, _ := memoryFixture(t)
	in := groundedInput(t, s, c, "alpha", "check-only", "刚挂掉。")
	model := &groundingSequenceModel{}
	e := &Engine{Store: s, Config: c, model: model}
	original := Decision{Action: "reply", Messages: []string{"通话刚结束了，有想说的可以告诉我。"}}
	out, err := e.DiagnoseGroundedReply(t.Context(), in, original)
	if err != nil || model.calls != 1 || out.Messages[0] != original.Messages[0] {
		t.Fatal(out, err, model.calls)
	}
	if model.inputs[0]["candidate_id"] != nil || model.inputs[0]["review_settings"] != nil || model.inputs[0]["review_units"] == nil {
		t.Fatal("model was asked to echo host identity or review settings missing")
	}
	if _, exists := model.inputs[0]["rejected_verdict"]; exists {
		t.Fatal("allowed candidate passed through editor")
	}
	before, _ := json.Marshal(out)
	if _, err := e.checkReplyGrounding(t.Context(), in, out); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(out)
	if string(before) != string(after) {
		t.Fatal("read-only checker mutated exact candidate")
	}
}

func TestGroundingRepairedCandidateRequiresFinalReadOnlyCheck(t *testing.T) {
	for _, required := range []bool{false, true} {
		s, c, _ := memoryFixture(t)
		in := groundedInput(t, s, c, "alpha", "final-check", "刚挂掉。")
		in.MustReply = required
		model := &groundingSequenceModel{checks: []string{"reject", "reject"}, answers: []string{`{"action":"reply","bubbles":[{"text":"另一条仍没有支持的个人描写。","route_id":"R1"}]}`}}
		e := &Engine{Store: s, Config: c, model: model}
		out, err := e.DiagnoseGroundedReply(t.Context(), in, Decision{Action: "reply", Messages: []string{"没有支持的个人描写，等一次修复。"}})
		if model.calls != 3 || model.checkCalls != 2 || model.answerCalls != 1 || len(out.Messages) > 0 {
			t.Fatal("unverified repaired text escaped", out, err, model.calls)
		}
		if required && err == nil || !required && (err != nil || out.Action != "silent") {
			t.Fatal(out, err, required)
		}
		initial, _ := json.Marshal(model.inputs[0]["candidate"])
		final, _ := json.Marshal(model.inputs[2]["candidate"])
		if string(initial) == string(final) {
			t.Fatal("edited candidate reused old verdict")
		}
	}
}

func TestLowRiskOrdinaryReplySkipsSemanticCalls(t *testing.T) {
	s, c, _ := memoryFixture(t)
	in := groundedInput(t, s, c, "alpha", "low-risk", "晚安")
	model := &groundingSequenceModel{}
	e := &Engine{Store: s, Config: c, model: model}
	out, err := e.DiagnoseGroundedReply(t.Context(), in, Decision{Action: "reply", Messages: []string{"晚安"}})
	if err != nil || out.Action != "reply" || model.calls != 0 {
		t.Fatal(out, err, model.calls)
	}
}

type mutatingVerdictModel struct{ mutate func() }

func (m mutatingVerdictModel) Chat(_ context.Context, _ string, turns []map[string]string) (string, error) {
	m.mutate()
	return syntheticGroundingVerdict(turns, "allow"), nil
}
func TestGroundingCheckRejectsEvidenceChangesDuringCall(t *testing.T) {
	s, c, _ := memoryFixture(t)
	in := groundedInput(t, s, c, "alpha", "changing-source", "明确来源。")
	source, ok := s.SourceForInput(in)
	if !ok {
		t.Fatal("missing source")
	}
	e := &Engine{Store: s, Config: c, model: mutatingVerdictModel{mutate: func() {
		_, err := s.db.Exec("UPDATE events SET revision=revision+1 WHERE id=?", source.EventID)
		if err != nil {
			t.Fatal(err)
		}
	}}}
	if _, err := e.checkReplyGrounding(t.Context(), in, Decision{Action: "reply", Messages: []string{"明确来源。"}}); err == nil {
		t.Fatal("verdict outlived source revision")
	}
}

func TestGroundingCheckAllowsIndependentAppendDuringModelCall(t *testing.T) {
	s, c, _ := memoryFixture(t)
	in := groundedInput(t, s, c, "alpha", "append-during-model", "明确来源。")
	e := &Engine{Store: s, Config: c, model: mutatingVerdictModel{mutate: func() { groundedInput(t, s, c, "beta", "ordinary-interruption", "另一位成员的普通插话。") }}}
	v, err := e.checkReplyGrounding(t.Context(), in, Decision{Action: "reply", Messages: []string{"明确来源。"}})
	if err != nil || v.Verdict != "allow" {
		t.Fatal("ordinary new message invalidated fixed original evidence", v, err)
	}
}

type snapshotMutationTools struct {
	ToolAccess
	mutate func()
}

func (p *snapshotMutationTools) NeedsPrivateReplyReview() bool                       { p.mutate(); return false }
func (*snapshotMutationTools) ReportPrivateDiagnostic(context.Context, string) error { return nil }
func TestGroundingSnapshotCannotRelabelChangedEvidence(t *testing.T) {
	s, c, _ := memoryFixture(t)
	in := groundedInput(t, s, c, "alpha", "snapshot-current", "明确来源。")
	model := &groundingSequenceModel{}
	e := &Engine{Store: s, Config: c, model: model, Tools: &snapshotMutationTools{mutate: func() {
		other := in
		other.TraceID = "changed-during-snapshot"
		other.Source = nil
		other.Text = "new synthetic event"
		if err := s.Observe(other, c, time.Now()); err != nil {
			t.Fatal(err)
		}
	}}}
	if _, err := e.checkReplyGrounding(t.Context(), in, Decision{Action: "reply", Messages: []string{"明确来源。"}}); err == nil || model.calls != 0 {
		t.Fatal("changed snapshot was relabeled or sent to model", err, model.calls)
	}
}

func TestStaleReplyProofCannotRecoverByDroppingSourceVersions(t *testing.T) {
	s, c, _ := memoryFixture(t)
	in := groundedInput(t, s, c, "alpha", "stale-proof", "原本的明确事实。")
	e := &Engine{Store: s, Config: c, model: &groundingSequenceModel{}}
	d, err := e.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{"原本的明确事实。"}})
	if err != nil {
		t.Fatal(err)
	}
	for id := range d.ReplyContexts[0].SourceVersions {
		if _, err = s.db.Exec("UPDATE events SET revision=revision+1 WHERE id=?", id); err != nil {
			t.Fatal(err)
		}
	}
	out, err := e.DiagnoseGroundedReply(t.Context(), in, d)
	if err == nil || len(out.Messages) > 0 || e.model.(*groundingSequenceModel).calls != 0 {
		t.Fatal("old source proof was replaced", out, err)
	}
}

type memoryAuthorizationFixture struct {
	ToolAccess
	stamp string
}

func (f *memoryAuthorizationFixture) MemoryResultAuthorized(stamp string) bool {
	return stamp != "" && stamp == f.stamp
}
func (f *memoryAuthorizationFixture) MemoryResultAuthorization() string { return f.stamp }

func TestQueuedReplyEvidenceSurvivesAppendButRejectsInvalidationOrRevocation(t *testing.T) {
	s, c, _ := memoryFixture(t)
	in := groundedInput(t, s, c, "alpha", "sealed-current", "你好")
	authority := &memoryAuthorizationFixture{stamp: "grant-v1"}
	e := &Engine{Store: s, Config: c, Tools: authority, model: &groundingSequenceModel{}}
	d, err := e.DiagnoseGroundedReply(t.Context(), in, Decision{Action: "reply", Messages: []string{"你好"}})
	if err != nil {
		t.Fatal(err)
	}
	r := d.ReplyContexts[0]
	if r.EvidenceEpoch == "" || r.EvidenceAuthorization != "grant-v1" {
		t.Fatal("new reply lacks evidence seal", r)
	}
	groundedInput(t, s, c, "beta", "independent-new-message", "无关插话。")
	if err = e.ValidateReplyContext(t.Context(), in, r); err != nil {
		t.Fatal("ordinary append invalidated reply", err)
	}
	authority.stamp = "grant-v2"
	if e.ValidateReplyContext(t.Context(), in, r) == nil {
		t.Fatal("authorization revoked but queued draft remains sendable")
	}
	authority.stamp = "grant-v1"
	if _, err = s.db.Exec("INSERT INTO meta(key,value) VALUES('recall_epoch','1') ON CONFLICT(key) DO UPDATE SET value=CAST(value AS INTEGER)+1"); err != nil {
		t.Fatal(err)
	}
	if e.ValidateReplyContext(t.Context(), in, r) == nil {
		t.Fatal("invalidated background evidence remained sendable")
	}
	if _, err = e.DiagnoseGroundedReply(t.Context(), in, d); err == nil {
		t.Fatal("old evidence was silently re-signed")
	}
	private := in
	private.IsGroup = false
	if e.ValidateReplyContext(t.Context(), private, r) == nil {
		t.Fatal("private target bypassed evidence seal")
	}
}
func TestContextRefreshRechecksMemoryAuthorizationWithoutDataChange(t *testing.T) {
	s, c, in := memoryFixture(t)
	_ = in
	authority := &memoryAuthorizationFixture{stamp: "grant-v1"}
	e := &Engine{Store: s, Config: c, Tools: authority}
	epoch, err := s.DataEpoch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range []bool{false, true} {
		var result map[string]any
		if page {
			result = map[string]any{"source": "untrusted_tool_result", "tool": "agent_global_search", "memory_epoch": epoch, "memory_authorization": "grant-v1", "text": "secret cross-conversation result", "result_id": "retained", "next_offset": 9}
		} else {
			result = map[string]any{"_memory_epoch": epoch, "_memory_authorization": "grant-v1", "items": []string{"secret cross-conversation result"}, "next_offset": 9}
		}
		tool := "memory_search"
		if page {
			tool = "tool_output_read"
		}
		makeTurn := func() []map[string]string {
			raw, _ := json.Marshal(result)
			wrapped, _ := json.Marshal(map[string]any{"source": "untrusted_tool_result", "tool": tool, "result": string(raw), "execution": map[string]any{"result_id": "original-call"}})
			return []map[string]string{{"role": "assistant", "content": "original call"}, {"role": "user", "content": string(wrapped)}}
		}
		authority.stamp = "grant-v1"
		valid := e.refreshMemoryPackets(t.Context(), makeTurn())
		raw, _ := json.Marshal(valid)
		if !strings.Contains(string(raw), "secret cross-conversation") {
			t.Fatal("valid result discarded", string(raw))
		}
		authority.stamp = "grant-v2"
		invalid := e.refreshMemoryPackets(t.Context(), makeTurn())
		raw, _ = json.Marshal(invalid)
		if len(invalid) != 2 || strings.Contains(string(raw), "secret cross-conversation") || !strings.Contains(string(raw), "original-call") || !strings.Contains(string(raw), "next_offset") {
			t.Fatal("revoked body or broken envelope", string(raw))
		}
	}
	ordinary := map[string]any{"source": "untrusted_tool_result", "tool": "server_exec", "memory_authorization": "", "text": "ordinary command output", "result_id": "command-result"}
	refreshed, _ := json.Marshal(e.refreshMemoryToolValue(t.Context(), ordinary))
	if !strings.Contains(string(refreshed), "ordinary command output") {
		t.Fatal("memory check affected ordinary output", string(refreshed))
	}
}
