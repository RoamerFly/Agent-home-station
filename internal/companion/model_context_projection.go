package companion

import (
	"bytes"
	"encoding/json"
)

// compactNativeModelEvidence changes only the model-facing copy. The original
// packets remain available to source checks, delivery checks and later reads.
func compactNativeModelEvidence(turns []map[string]string) []map[string]string {
	out := make([]map[string]string, len(turns))
	copy(out, turns)
	for i, turn := range turns {
		if turn["role"] != "user" {
			continue
		}
		var packet map[string]json.RawMessage
		if json.Unmarshal([]byte(turn["content"]), &packet) != nil {
			continue
		}
		var kind string
		if json.Unmarshal(packet["kind"], &kind) != nil {
			continue
		}
		switch kind {
		case "current_message":
			compactModelQuote(packet)
			for _, key := range []string{"task_id", "request_trace_id", "origin_trace_id", "origin_thread", "relation_notes", "message_time", "recovery_tool_results", "planned_reviews", "recent_media_focus", "mentioned_ids", "addressees"} {
				if emptyModelOptional(packet[key]) {
					delete(packet, key)
				}
			}
			for _, key := range []string{"task_continuation", "background_result", "review_required", "historical_review", "surf", "surf_force_share"} {
				if bytes.Equal(bytes.TrimSpace(packet[key]), []byte("false")) {
					delete(packet, key)
				}
			}
		case "conversation_context":
			var timeline []json.RawMessage
			if json.Unmarshal(packet["timeline"], &timeline) != nil {
				continue
			}
			for j, raw := range timeline {
				var row map[string]json.RawMessage
				if json.Unmarshal(raw, &row) != nil {
					continue
				}
				if compactModelQuote(row) {
					timeline[j], _ = json.Marshal(row)
				}
			}
			packet["timeline"], _ = json.Marshal(timeline)
		default:
			continue
		}
		encoded, err := json.Marshal(packet)
		if err != nil {
			continue
		}
		copyTurn := make(map[string]string, len(turn))
		for key, value := range turn {
			copyTurn[key] = value
		}
		copyTurn["content"] = string(encoded)
		out[i] = copyTurn
	}
	return out
}

func compactModelQuote(row map[string]json.RawMessage) bool {
	quoted := row["quoted_source"]
	if len(quoted) == 0 || bytes.Equal(bytes.TrimSpace(quoted), []byte("null")) {
		return false
	}
	var chain map[string]json.RawMessage
	if json.Unmarshal(row["quote_chain"], &chain) != nil {
		return false
	}
	if !bytes.Equal(bytes.TrimSpace(chain["quoted_source"]), bytes.TrimSpace(quoted)) {
		return false
	}
	delete(chain, "quoted_source")
	row["quote_chain"], _ = json.Marshal(chain)
	return true
}

func emptyModelOptional(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte(`""`)) || bytes.Equal(trimmed, []byte("[]"))
}
