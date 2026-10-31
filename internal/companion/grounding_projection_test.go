package companion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type projectedEditorModel struct {
	calls      int
	wire       []string
	invalidRef string
}

func (m *projectedEditorModel) Chat(_ context.Context, policy string, turns []map[string]string) (string, error) {
	m.calls++
	m.wire = append(m.wire, turns[0]["content"])
	if policy == groundingCheckPolicy {
		verdict := "allow"
		if m.calls == 1 {
			verdict = "reject"
		}
		return syntheticGroundingVerdict(turns, verdict), nil
	}
	ref := "R1"
	if m.invalidRef != "" {
		ref = m.invalidRef
	}
	answer, _ := json.Marshal(routeEditedReply{Action: "reply", Bubbles: []editedReplyBubble{{Text: "收到。", RouteID: ref}}})
	return string(answer), nil
}

func TestGroundingCheckEditorAndFinalUseOnlyLocalMetadataAliases(t *testing.T) {
	for _, invalid := range []string{"", "S99999", "K1", "raw-source-id"} {
		name := invalid
		if name == "" {
			name = "valid-alias"
		}
		t.Run(name, func(t *testing.T) {
			s, c, _ := memoryFixture(t)
			in := groundedInput(t, s, c, "alpha", "projected-editor", "请核对这条记录。")
			source, ok := s.SourceForInput(in)
			if !ok {
				t.Fatal("source missing")
			}
			ref := invalid
			if invalid == "raw-source-id" {
				ref = source.EventID
			}
			model := &projectedEditorModel{invalidRef: ref}
			e := &Engine{Store: s, Config: c, model: model}
			out, err := e.ReviewGroundedReply(t.Context(), in, Decision{Action: "reply", Messages: []string{"待核验文字。"}, ReplyContexts: []ReplyContext{{RiskFlags: []string{"projection_protocol_test"}}}})
			if invalid == "" {
				if err != nil || out.Action != "reply" || model.calls != 3 || out.ReplyContexts[0].SourceEventIDs[0] != source.EventID {
					t.Fatal(out, err, model.calls)
				}
			} else if out.Action != "silent" || model.calls != 2 || len(out.Messages) != 0 {
				t.Fatal("invalid editor source escaped", out, err, model.calls)
			}
			for _, wire := range model.wire {
				if strings.Contains(wire, source.EventID) || strings.Contains(wire, `"source_aliases"`) || strings.Contains(wire, `"original_state"`) || strings.Contains(wire, `"review_binding"`) {
					t.Fatal("host-only source map leaked into a model call", wire)
				}
			}
		})
	}
}

func TestGroundingEditorCannotPromoteHostOrExecutionRefsToDBSources(t *testing.T) {
	binding := &GroundingReviewBinding{SourceAliases: map[string]string{"S1": strings.Repeat("c", 64), "S2": "execution:tool", "K1": "K1"}}
	for _, ref := range []string{"S2", "K1", "S99", strings.Repeat("c", 64)} {
		for _, p := range []ReplyContext{{SourceEventIDs: []string{ref}}, {AnchorEventID: ref}, {SourceVersions: map[string]int{ref: 1}}} {
			if _, err := restoreGroundingReplyContexts([]ReplyContext{p}, binding); err == nil {
				t.Fatal("non-source or raw identity became DB provenance", ref, p)
			}
		}
	}
}

func TestGroundingProjectionOnlyChangesStructuralSourceReferences(t *testing.T) {
	id := strings.Repeat("a", 64)
	words := "原文包含来源标识 " + id + "，不要改变这段话。"
	packet, _ := json.Marshal(map[string]any{"kind": "history_message", "source_event_id": id, "text": words, "source": map[string]any{"event_id": id, "body_hash": "private-integrity-hash"}})
	state := map[string]any{"evidence_event_ids": []string{id}, "candidate": Decision{Action: "reply", Messages: []string{words}}, "sources": []map[string]string{{"role": "user", "content": string(packet)}}, "execution_evidence": []map[string]any{{"evidence_id": id, "call": map[string]any{"arguments": map[string]any{"text": words, "event_id": id}}, "result": map[string]any{"text": words, "event_id": id}}}}
	before, _ := json.Marshal(state)
	binding, refs, err := projectGroundingState(state)
	if err != nil || len(refs) != 1 || refs[0] != "S1" || binding.SourceAliases["S1"] != id {
		t.Fatal(binding, refs, err)
	}
	var projected map[string]any
	if err := json.Unmarshal(binding.ReviewInput, &projected); err != nil {
		t.Fatal(err)
	}
	candidate := projected["candidate"].(map[string]any)
	if candidate["messages"].([]any)[0] != words {
		t.Fatal("candidate was rewritten")
	}
	row := projected["sources"].([]any)[0].(map[string]any)["content"].(map[string]any)
	if row["text"] != words || row["source_event_id"] != "S1" || row["source"].(map[string]any)["event_id"] != "S1" {
		t.Fatal("literal and metadata were not kept separate", row)
	}
	if _, present := row["source"].(map[string]any)["body_hash"]; present {
		t.Fatal("unneeded integrity hash remained in model packet")
	}
	result := projected["execution_evidence"].([]any)[0].(map[string]any)["result"].(map[string]any)
	if result["text"] != words || result["event_id"] != id {
		t.Fatal("opaque external result was rewritten", result)
	}
	after, _ := json.Marshal(state)
	if string(before) != string(after) || !strings.Contains(string(binding.OriginalState), id) {
		t.Fatal("projection mutated authoritative state or lost host provenance")
	}
}

func TestRequestKnowledgeMatchesRequestTaskAndAssetWithoutInferringExternalAbsence(t *testing.T) {
	in := Input{Account: "test", Thread: "one", TraceID: "request-one", TaskID: "task-one"}
	pair := func(ref, asset, state string) map[string]any {
		return map[string]any{"account": in.Account, "thread": in.Thread, "request_message_id": in.TraceID, "task_id": in.TaskID, "tool": "wechat_delivery_status", "evidence_id": ref, "execution": map[string]any{"status": "succeeded"}, "call": map[string]any{"arguments": map[string]any{"asset_id": asset}}, "result": map[string]any{"status": state, "asset_id": asset, "receipt_id": "receipt-for-" + asset}}
	}
	generated := pair("execution:generated", "asset-a", "generated")
	acceptedB := pair("execution:accepted-b", "asset-b", "accepted")
	wrongTask := pair("execution:wrong-task", "asset-a", "accepted")
	wrongTask["task_id"] = "other-task"
	unknown := pair("execution:unparsed", "asset-a", "accepted")
	unknown["result"] = "unparsed preview, not a verified receipt"
	state := requestKnowledgeState(in, []map[string]any{generated, acceptedB, wrongTask, unknown}, time.Now())
	if state["external_delivery_status"] != "unknown" || state["coverage"] != "current_request_visible_results" || state["result_coverage"] != "bounded_or_unparsed_visible_results" {
		t.Fatal("knowledge state pretended external or exhaustive knowledge", state)
	}
	assets := state["asset_states"].([]map[string]any)
	if len(assets) != 2 || assets[0]["asset_id"] != "asset-a" || assets[0]["receipt_evidence"] != "not_observed_in_this_snapshot" || assets[1]["receipt_evidence"] != "observed_in_this_snapshot" {
		t.Fatal("another asset or task supplied this asset's receipt", assets)
	}
	acceptedA := pair("execution:accepted-a", "asset-a", "accepted")
	state = requestKnowledgeState(in, []map[string]any{generated, acceptedA}, time.Now())
	if state["receipt_evidence"] != "observed_in_this_snapshot" || state["asset_states"].([]map[string]any)[0]["receipt_evidence"] != "observed_in_this_snapshot" {
		t.Fatal("matching accepted result did not update evidence availability", state)
	}
	conflicting := pair("execution:mismatch", "asset-a", "accepted")
	conflicting["result"].(map[string]any)["asset_id"] = "asset-b"
	state = requestKnowledgeState(in, []map[string]any{conflicting}, time.Now())
	if state["asset_states"].([]map[string]any)[0]["receipt_evidence"] != "not_observed_in_this_snapshot" {
		t.Fatal("result for a different asset was assigned to the requested asset", state)
	}
	external := pair("execution:external", "asset-a", "accepted")
	external["tool"] = "web_read"
	state = requestKnowledgeState(in, []map[string]any{external}, time.Now())
	if state["receipt_evidence"] != "not_observed_in_this_snapshot" {
		t.Fatal("an external page was promoted to native delivery evidence", state)
	}
}
