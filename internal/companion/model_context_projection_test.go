package companion

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeModelEvidenceCompactsOnlyMatchingCopies(t *testing.T) {
	const original = `{"kind":"current_message","message_id":"M","text":"看这条","source":{"message_id":"M","sequence":9007199254740993},"quoted_source":{"message_id":"Q","sender":"other"},"quote_chain":{"quoted_by_id":"me","quoted_source":{"message_id":"Q","sender":"other"}},"task_id":"","task_continuation":false,"recovery_tool_results":[],"must_reply":false}`
	turns := []map[string]string{{"role": "user", "content": original}}
	projected := compactNativeModelEvidence(turns)
	var packet map[string]json.RawMessage
	if json.Unmarshal([]byte(projected[0]["content"]), &packet) != nil || !strings.Contains(projected[0]["content"], "9007199254740993") {
		t.Fatal("model projection changed source identity or numeric precision")
	}
	if packet["task_id"] != nil || packet["task_continuation"] != nil || packet["recovery_tool_results"] != nil || packet["must_reply"] == nil {
		t.Fatal("optional fields or routing flags were changed")
	}
	var chain map[string]json.RawMessage
	if json.Unmarshal(packet["quote_chain"], &chain) != nil || chain["quoted_source"] != nil || chain["quoted_by_id"] == nil {
		t.Fatal("matching quote was not compacted safely")
	}
	if turns[0]["content"] != original || !strings.Contains(turns[0]["content"], `"quote_chain":{"quoted_by_id":"me","quoted_source"`) {
		t.Fatal("source packet was mutated")
	}
}

func TestNativeModelEvidenceKeepsDistinctQuoteAndTimeline(t *testing.T) {
	const original = `{"kind":"conversation_context","scope":"S","timeline":[{"source_event_id":"E","quoted_source":{"event_id":"Q","revision":2},"quote_chain":{"quoted_source":{"event_id":"Q","revision":1},"quoted_author_id":"other"},"attachment_delivery":{"delivery_state":"partial","accepted_parts":1,"total_parts":2}}]}`
	projected := compactNativeModelEvidence([]map[string]string{{"role": "user", "content": original}})
	if projected[0]["content"] == "" || !strings.Contains(projected[0]["content"], `"revision":1`) || !strings.Contains(projected[0]["content"], `"accepted_parts":1`) || !strings.Contains(projected[0]["content"], `"total_parts":2`) {
		t.Fatal("distinct quote or partial delivery was dropped")
	}
}

func TestNativeTimelinePreservesSourceDifferencesAndReceipts(t *testing.T) {
	const original = `{"kind":"conversation_context","scope":"room","timeline":[
		{"source_event_id":"E","message_id":"M","revision":2,"message_sender_id":"S","text":"first","source":{"event_id":"E","message_id":"M","instance_id":"A","account":"X","thread":"T","sender":"S","revision":2,"integrity":"verified","body_hash":"host-proof","sequence":9007199254740993},"personal_context_redacted":false},
		{"source_event_id":"E2","revision":3,"message_id":"M2","message_sender_id":"S","source":{"event_id":"different","message_id":"M3","instance_id":"B","account":"X","thread":"T","sender":"other","revision":1,"integrity":"conflicted","body_hash":"conflict-proof"},"personal_context_redacted":true,"quote_chain":{"quoted_author_id":"other"},"attachment_delivery":{"receipt_state":"partially_accepted","accepted_parts":1,"total_parts":2}},
		{"source_event_id":"legacy","message_id":"legacy","text":"no source"}
	]}`
	turns := []map[string]string{{"role": "user", "content": original}}
	projected := compactNativeModelEvidence(turns)
	var view struct {
		Shared   map[string]string            `json:"common_timeline_source_fields"`
		Timeline []map[string]json.RawMessage `json:"timeline"`
	}
	if err := json.Unmarshal([]byte(projected[0]["content"]), &view); err != nil || len(view.Timeline) != 3 {
		t.Fatalf("invalid projected timeline: %v", err)
	}
	if turns[0]["content"] != original || view.Shared["account"] != "X" || view.Shared["thread"] != "T" || view.Shared["instance_id"] != "" {
		t.Fatal("host input or shared source identity changed")
	}
	first, conflict := view.Timeline[0], view.Timeline[1]
	if string(first["source_event_id"]) != `"E"` || string(first["revision"]) != "2" || string(first["message_id"]) != `"M"` || first["personal_context_redacted"] != nil ||
		string(first["source"]) != `{"instance_id":"A","integrity":"verified","sequence":9007199254740993}` {
		t.Fatalf("incorrect projected verified row: %s", first)
	}
	if !strings.Contains(string(conflict["source"]), `"body_hash":"conflict-proof"`) || !strings.Contains(string(conflict["source"]), `"event_id":"different"`) ||
		!strings.Contains(string(conflict["source"]), `"revision":1`) || string(conflict["revision"]) != "3" || string(conflict["personal_context_redacted"]) != "true" ||
		!strings.Contains(string(conflict["attachment_delivery"]), `"receipt_state":"partially_accepted"`) || !strings.Contains(string(conflict["quote_chain"]), `"quoted_author_id":"other"`) || view.Timeline[2]["source"] != nil {
		t.Fatalf("conflict, redaction, quote, partial receipt or legacy source was damaged: %s", projected[0]["content"])
	}
}

func TestNativeTimelineKeepsUnmarkedRevisionConflict(t *testing.T) {
	raw := `{"source_event_id":"E","revision":2,"source":{"event_id":"E","revision":1,"body_hash":"original","received_at":"earlier","integrity":"verified"}}`
	var row map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &row); err != nil {
		t.Fatal(err)
	}
	compactModelTimeline([]map[string]json.RawMessage{row})
	if !strings.Contains(string(row["source"]), `"body_hash":"original"`) || !strings.Contains(string(row["source"]), `"received_at":"earlier"`) {
		t.Fatalf("lost the original identity of a different revision: %s", row["source"])
	}
}

func TestNativeTimelineKeepsDifferentReceiptTime(t *testing.T) {
	raw := `{"source_event_id":"E","message_id":"M","message_sender_id":"S","at":"2026-11-25T08:00:00Z","source":{"event_id":"E","message_id":"M","sender":"S","received_at":"2026-11-25T09:00:00Z","integrity":"verified"}}`
	var row map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &row); err != nil {
		t.Fatal(err)
	}
	compactModelTimeline([]map[string]json.RawMessage{row})
	if !strings.Contains(string(row["source"]), `"received_at":"2026-11-25T09:00:00Z"`) {
		t.Fatalf("delayed receipt time was lost: %s", row["source"])
	}
}
