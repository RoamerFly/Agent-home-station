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
