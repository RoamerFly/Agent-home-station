package companion

import (
	"encoding/json"
	"strings"
	"testing"
)

func executionFixture(t *testing.T, result any) (*Engine, Input) {
	t.Helper()
	s, c, _ := memoryFixture(t)
	in := groundedInput(t, s, c, "alpha", "execution-current", "请按指定声线完成这项任务。")
	in.ExpectedVoiceID = "voice-mom"
	model := &groundingSequenceModel{}
	e := &Engine{Store: s, Config: c, model: model, prepared: &preparedAuditContext{}}
	current, _ := json.Marshal(map[string]any{"kind": "current_message", "channel": in.Channel, "account": in.Account, "thread": in.Thread, "speaker_id": in.Sender, "message_id": in.TraceID, "source": in.Source, "text": in.Text})
	call := `{"action":"tool","tool":"speech_synthesize","arguments":{"voice_id":"voice-mom","text":"合成资料。","api_key":"must-be-redacted"}}`
	body, _ := json.Marshal(result)
	feedback, _ := json.Marshal(map[string]any{"source": "untrusted_tool_result", "tool": "speech_synthesize", "result": string(body), "execution": map[string]any{"status": "succeeded", "result_id": "synthesis-result-1"}})
	e.capturePreparedContext("system", []map[string]string{{"role": "user", "content": string(current)}, {"role": "assistant", "content": call}, {"role": "user", "content": string(feedback)}}, nil)
	return e, in
}

func TestExecutionEvidencePreservesActualStatusReceiptsAndVoiceIdentity(t *testing.T) {
	for _, status := range []string{"queued", "accepted", "generated"} {
		e, in := executionFixture(t, map[string]any{"status": status, "job_id": "job-1", "receipt_id": "receipt-1", "media": []map[string]any{{"id": "audio-1", "voice_id": "voice-mom", "voice_profile_id": "profile-mom", "source_asset_id": "video-1"}}})
		rows := e.preparedExecutionEvidence(t.Context(), in)
		if len(rows) != 1 {
			t.Fatal(status, rows)
		}
		raw, _ := json.Marshal(rows)
		for _, required := range []string{status, "receipt-1", "audio-1", "voice-mom", "profile-mom", "video-1", "synthesis-result-1", "execution:"} {
			if !strings.Contains(string(raw), required) {
				t.Fatal("lost actual execution evidence", required, string(raw))
			}
		}
		if strings.Contains(string(raw), "must-be-redacted") {
			t.Fatal("credentials copied into checker", string(raw))
		}
		if _, err := e.checkReplyGrounding(t.Context(), in, Decision{Action: "reply", Messages: []string{"这是有来源的合成任务状态。"}}); err != nil {
			t.Fatal(err)
		}
		input := e.model.(*groundingSequenceModel).inputs[0]
		if len(atomicTypedRows(input, "execution_result")) != 1 || input["expected_voice_id"] != "voice-mom" {
			t.Fatal("checker cannot see actual execution and selected voice", input)
		}
	}
}

func TestExecutionEvidenceDoesNotTurnDefaultVoiceOrMissingReceiptIntoRequestedVoice(t *testing.T) {
	e, in := executionFixture(t, map[string]any{"status": "generated", "media": []map[string]any{{"id": "audio-default", "voice_id": "voice-default", "voice_profile_id": "default-profile"}}})
	state := e.groundingReviewState(t.Context(), in, Decision{ExpectedVoiceID: "voice-default"}, false)
	rows := state["execution_evidence"].([]map[string]any)
	raw, _ := json.Marshal(rows[0]["result"])
	if state["expected_voice_id"] != "voice-mom" || !strings.Contains(string(raw), "voice-default") || strings.Contains(string(raw), `"voice_id":"voice-mom"`) {
		t.Fatal("default voice mislabeled", string(raw))
	}
	// No pair is not a completed execution. Nor can a stale snapshot from a
	// different message/account/thread be used to justify this turn's draft.
	for _, changed := range []Input{func() Input { x := in; x.TraceID = "other-turn"; return x }(), func() Input { x := in; x.Account = "other-account"; return x }(), func() Input { x := in; x.Thread = "another-room"; return x }()} {
		if len(e.preparedExecutionEvidence(t.Context(), changed)) != 0 {
			t.Fatal("another turn's cached receipt used", changed)
		}
	}
	e.clearPreparedContext()
	if len(e.preparedExecutionEvidence(t.Context(), in)) != 0 {
		t.Fatal("missing result became execution evidence")
	}
}

func TestExecutionEvidenceRequiresActualCallResultPairAndCurrentAuthorization(t *testing.T) {
	e, in := executionFixture(t, map[string]any{"status": "queued"})
	var snapshot struct {
		Messages []map[string]string `json:"messages"`
	}
	_ = json.Unmarshal(e.preparedContext(), &snapshot)
	snapshot.Messages[1]["content"] = `{"action":"tool","tool":"unrelated_tool","arguments":{}}`
	e.capturePreparedContext("system", snapshot.Messages, nil)
	if len(e.preparedExecutionEvidence(t.Context(), in)) != 0 {
		t.Fatal("unpaired result accepted")
	}
	authority := &memoryAuthorizationFixture{stamp: "current-grant"}
	e.Tools = authority
	epoch, err := e.Store.DataEpoch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result, _ := json.Marshal(map[string]any{"_memory_epoch": epoch, "_memory_authorization": "revoked-grant", "text": "revoked private body", "next_offset": 5})
	feedback, _ := json.Marshal(map[string]any{"source": "untrusted_tool_result", "tool": "memory_read", "result": string(result), "execution": map[string]any{"status": "succeeded", "result_id": "memory-result"}})
	snapshot.Messages[1]["content"] = `{"action":"tool","tool":"memory_read","arguments":{"id":"memory-id"}}`
	snapshot.Messages[2]["content"] = string(feedback)
	e.capturePreparedContext("system", snapshot.Messages, nil)
	raw, _ := json.Marshal(e.preparedExecutionEvidence(t.Context(), in))
	if strings.Contains(string(raw), "revoked private body") || !strings.Contains(string(raw), "invalidated") || !strings.Contains(string(raw), "memory-result") {
		t.Fatal("checker received revoked source or lost action identity", string(raw))
	}
}
