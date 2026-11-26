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
			var timeline []map[string]json.RawMessage
			if json.Unmarshal(packet["timeline"], &timeline) != nil {
				continue
			}
			if shared := compactModelTimeline(timeline); len(shared) > 0 {
				packet["common_timeline_source_fields"], _ = json.Marshal(shared)
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

// Keep the event handle on each row for audits and exact reads. Only the
// model copy omits host verification material, and only for matching events.
func compactModelTimeline(rows []map[string]json.RawMessage) map[string]json.RawMessage {
	var shared map[string]json.RawMessage
	var sources []map[string]json.RawMessage
	var sourceRows []int
	for i, row := range rows {
		compactModelQuote(row)
		for _, key := range []string{"text_excerpted", "personal_context_redacted"} {
			if string(row[key]) == "false" {
				delete(row, key)
			}
		}
		for _, key := range []string{"message_sender_name", "message_sender_name_source", "speaker", "speaker_name_source"} {
			if string(row[key]) == `""` {
				delete(row, key)
			}
		}
		if len(row["speaker_id"]) > 0 && bytes.Equal(row["speaker_id"], row["message_sender_id"]) {
			// The speaker binding is meaningful even when it matches the sender.
			// Keep the ID and eliminate only matching display labels.
			dropMatchingModelField(row, "speaker", row["message_sender_name"])
			dropMatchingModelField(row, "speaker_name_source", row["message_sender_name_source"])
		}
		var source map[string]json.RawMessage
		if json.Unmarshal(row["source"], &source) != nil || source == nil {
			continue
		}
		// A scope can contain records imported from different instances.
		// Lift only field values shared by every source object in this packet.
		if len(sources) == 0 {
			shared = make(map[string]json.RawMessage)
			for _, k := range []string{"instance_id", "account", "thread"} {
				if !emptyModelOptional(source[k]) {
					shared[k] = source[k]
				}
			}
		} else {
			for k, v := range shared {
				if !bytes.Equal(v, source[k]) {
					delete(shared, k)
				}
			}
		}
		sources = append(sources, source)
		sourceRows = append(sourceRows, i)
		if emptyModelOptional(row["source_event_id"]) || !bytes.Equal(source["event_id"], row["source_event_id"]) || string(source["integrity"]) == `"conflicted"` {
			continue
		}
		// Distinct revision or adapter identities remain inspectable, including
		// the original hash. This also covers older unmarked conflicts.
		if differingModelFields(source["message_id"], row["message_id"]) || differingModelFields(source["revision"], row["revision"]) || differingModelFields(source["sender"], row["message_sender_id"]) {
			continue
		}
		delete(source, "body_hash")
		// A delayed delivery can have a different receipt time from the event.
		// Keep both times unless the row already carries that exact value.
		dropMatchingModelField(source, "received_at", row["at"])
		for sourceKey, rowKey := range map[string]string{"event_id": "source_event_id", "message_id": "message_id", "sender": "message_sender_id", "sender_name": "message_sender_name", "occurred_at": "at", "revision": "revision"} {
			dropMatchingModelField(source, sourceKey, row[rowKey])
		}
	}
	if len(sources) < 2 {
		shared = nil
	}
	for i, source := range sources {
		for k := range shared {
			delete(source, k)
		}
		rows[sourceRows[i]]["source"], _ = json.Marshal(source)
	}
	return shared
}

func differingModelFields(a, b json.RawMessage) bool {
	return !emptyModelOptional(a) && !emptyModelOptional(b) && !bytes.Equal(a, b)
}

func dropMatchingModelField(fields map[string]json.RawMessage, key string, other json.RawMessage) {
	if len(other) > 0 && bytes.Equal(fields[key], other) {
		delete(fields, key)
	}
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
