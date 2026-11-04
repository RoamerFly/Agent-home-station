package companion

import (
	"context"
	"encoding/json"
	"strings"
)

// A memory tool can forget or correct an entry during a running Agent turn.
// Revalidate dynamic packets before the next model call. Preserve the current
// request, effective followups and tool/result envelopes in full.
func (e *Engine) refreshMemoryPackets(ctx context.Context, turns []map[string]string) []map[string]string {
	if e.Store.db == nil {
		return turns
	}
	for i, turn := range turns {
		if turn == nil {
			continue
		}
		var packet map[string]json.RawMessage
		if json.Unmarshal([]byte(turn["content"]), &packet) != nil {
			continue
		}
		var kind string
		_ = json.Unmarshal(packet["kind"], &kind)
		if turn["role"] != "user" && !(turn["role"] == "assistant" && kind == "history_reply") {
			continue
		}
		var source, tool string
		_ = json.Unmarshal(packet["source"], &source)
		_ = json.Unmarshal(packet["tool"], &tool)
		if source == "untrusted_tool_result" && (strings.HasPrefix(tool, "memory_") || tool == "agent_global_search" || tool == "tool_output_read") {
			var raw string
			_ = json.Unmarshal(packet["result"], &raw)
			var value any
			if json.Unmarshal([]byte(raw), &value) == nil {
				if memoryQueryResult(tool) && !e.memoryToolValueAuthorized(value) {
					value = invalidatedMemoryResult(value, "Memory authorization changed or is missing; issue a new authorized query before using this result.")
				} else {
					value = e.refreshMemoryToolValue(ctx, value)
				}
				rawBytes, _ := json.Marshal(value)
				packet["result"], _ = json.Marshal(string(rawBytes))
			} else if strings.HasPrefix(tool, "memory_") || tool == "agent_global_search" {
				packet["result"], _ = json.Marshal("Memory preview cannot be version-validated; read its current source/page again. The original operation was not repeated.")
			}
			b, _ := json.Marshal(packet)
			turns[i] = map[string]string{"role": turn["role"], "content": string(b)}
			continue
		}
		changed := false
		switch kind {
		case "current_followup":
			var rows []map[string]any
			if json.Unmarshal(packet["events"], &rows) == nil {
				for _, row := range rows {
					e.refreshEmbeddedEvidence(ctx, row)
				}
				packet["events"], _ = json.Marshal(rows)
				changed = true
			}
		case "current_message":
			if value := packet["quoted_message_evidence"]; len(value) > 0 {
				var row map[string]any
				if json.Unmarshal(value, &row) == nil {
					e.refreshEmbeddedEvidence(ctx, row)
					packet["quoted_message_evidence"], _ = json.Marshal(row)
					changed = true
				}
			}
			var unread []map[string]any
			if json.Unmarshal(packet["unread_messages"], &unread) == nil && len(unread) > 0 {
				for _, row := range unread {
					e.refreshEmbeddedEvidence(ctx, row)
				}
				packet["unread_messages"], _ = json.Marshal(unread)
				changed = true
			}
		case "conversation_summary":
			var scope, epoch, fingerprint string
			_ = json.Unmarshal(packet["scope"], &scope)
			_ = json.Unmarshal(packet["data_epoch"], &epoch)
			_ = json.Unmarshal(packet["summary_fingerprint"], &fingerprint)
			currentEpoch, err := e.Store.DataEpoch(ctx)
			e.Store.mu.Lock()
			current := e.Store.data.Scopes[scope]
			e.Store.mu.Unlock()
			if scope == "" || epoch == "" || err != nil || epoch != currentEpoch || current.Summary == "" || fingerprint != summaryFingerprint(current.Summary) {
				turns[i] = nil
				continue
			}
			if !e.validSummaryPacketSources(ctx, scope, packet["data"]) {
				turns[i] = nil
				continue
			}
		case "long_term_memory":
			var rows []map[string]any
			if json.Unmarshal(packet["items"], &rows) != nil {
				continue
			}
			kept := []map[string]any{}
			limits := e.memorySettings()
			used, coreUsed := 0, 0
			for _, row := range rows {
				id, _ := row["id"].(string)
				scope, _ := row["scope"].(string)
				m, err := e.Store.ReadMemory(ctx, []string{scope}, id)
				if err != nil {
					continue
				}
				m.trimSearchPreview()
				remaining := limits.RecallTokens - used
				if m.Pinned {
					remaining = min(remaining, limits.ContinuityTokens-coreUsed)
				}
				updated, cost := recalledMemoryRow(&m, remaining)
				if cost > remaining {
					continue
				}
				if m.Pinned {
					coreUsed += cost
				}
				used += cost
				kept = append(kept, updated)
			}
			packet["items"], _ = json.Marshal(kept)
			packet["token_estimate"], _ = json.Marshal(used)
			changed = true
		case "conversation_context":
			var scope string
			_ = json.Unmarshal(packet["scope"], &scope)
			if scope == "" {
				continue
			}
			var rows []map[string]any
			if json.Unmarshal(packet["timeline"], &rows) != nil {
				continue
			}
			kept := []map[string]any{}
			for _, row := range rows {
				id, _ := row["source_event_id"].(string)
				if id != "" {
					ev, err := e.Store.ReadEvent(ctx, []string{scope}, id)
					if err != nil {
						continue
					}
					e.refreshEvidenceRow(row, ev)
				}
				if quoted, ok := row["quoted_message"].(map[string]any); ok {
					e.refreshEmbeddedEvidence(ctx, quoted)
					row["quoted_message"] = quoted
				}
				kept = append(kept, row)
			}
			packet["timeline"], _ = json.Marshal(kept)
			e.Store.mu.Lock()
			current := e.Store.data.Scopes[scope]
			e.Store.mu.Unlock()
			if current.Summary == "" {
				packet["summary"] = json.RawMessage("null")
			}
			changed = true
		case "history_message", "history_reply":
			var id, scope string
			_ = json.Unmarshal(packet["source_event_id"], &id)
			_ = json.Unmarshal(packet["scope"], &scope)
			if id != "" && scope != "" {
				ev, err := e.Store.ReadEvent(ctx, []string{scope}, id)
				if err != nil {
					turns[i] = nil
					continue
				}
				var row map[string]any
				encoded, _ := json.Marshal(packet)
				_ = json.Unmarshal(encoded, &row)
				e.refreshEvidenceRow(row, ev)
				if quoted, ok := row["quoted_message"].(map[string]any); ok {
					e.refreshEmbeddedEvidence(ctx, quoted)
					row["quoted_message"] = quoted
				}
				if kind == "history_reply" {
					if redacted, _ := row["personal_context_redacted"].(bool); !redacted {
						if ev.Media != nil {
							row["timeline_event"] = "assistant_attachment"
							row["attachment_delivery"] = mediaDeliveryEvidence(*ev.Media)
							delete(row, "media_delivery")
							if ev.Text == "[媒体附件] "+ev.Media.Title {
								delete(row, "messages")
								delete(row, "text")
								row["attachment_only"] = true
							} else {
								row["messages"] = []string{ev.Text}
							}
						} else {
							row["messages"] = []string{ev.Text}
						}
					} else {
						row["messages"] = []string{}
						delete(row, "media_delivery")
						delete(row, "attachment_delivery")
					}
				}
				encoded, _ = json.Marshal(row)
				// Unmarshal into a fresh map. Reusing packet would retain fields
				// deliberately deleted by refreshEvidenceRow (for example a
				// redacted member's former media observation).
				packet = map[string]json.RawMessage{}
				_ = json.Unmarshal(encoded, &packet)

				changed = true
			}
		}
		if changed {
			b, _ := json.Marshal(packet)
			turns[i] = map[string]string{"role": turn["role"], "content": string(b)}
		}
	}
	kept := turns[:0]
	for _, turn := range turns {
		if turn != nil {
			kept = append(kept, turn)
		}
	}
	return kept
}

func (e *Engine) refreshEmbeddedEvidence(ctx context.Context, row map[string]any) {
	id, _ := row["source_event_id"].(string)
	scope, _ := row["scope"].(string)
	if id != "" && scope != "" {
		if ev, err := e.Store.ReadEvent(ctx, []string{scope}, id); err == nil {
			e.refreshEvidenceRow(row, ev)
			return
		}
		for _, key := range []string{"text", "literal_user_text", "media_observations", "incoming_media", "runtime_context"} {
			delete(row, key)
		}
		row["text"] = ""
		row["user_text_state"] = "invalidated"
		row["invalidated"] = true
	}
}

func (e *Engine) refreshMemoryToolValue(ctx context.Context, value any) any {
	switch x := value.(type) {
	case []any:
		for i := range x {
			x[i] = e.refreshMemoryToolValue(ctx, x[i])
		}
		return x
	case string:
		var nested any
		if json.Unmarshal([]byte(x), &nested) == nil {
			b, _ := json.Marshal(e.refreshMemoryToolValue(ctx, nested))
			return string(b)
		}
		return x
	case map[string]any:
		if _, exists := x["_memory_authorization"]; exists && !e.memoryToolValueAuthorized(x) {
			return invalidatedMemoryResult(x, "Memory authorization changed; query the current authorized scope again.")
		}
		if stamp, exists := x["_memory_epoch"]; exists {
			epoch, _ := stamp.(string)
			current, err := e.Store.DataEpoch(ctx)
			if epoch == "" || err != nil || epoch != current {
				clean := map[string]any{"invalidated": true, "instruction": "Memory changed since this result was read; query the current source again."}
				for _, key := range []string{"result_id", "next_offset", "next_cursor", "next_source_offset", "scope", "id"} {
					if v, ok := x[key]; ok {
						clean[key] = v
					}
				}
				return clean
			}
		}
		packetSource, _ := x["source"].(string)
		packetTool, _ := x["tool"].(string)
		if packetSource == "untrusted_tool_result" && (strings.HasPrefix(packetTool, "memory_") || packetTool == "agent_global_search") {
			if memoryQueryResult(packetTool) {
				stamp, _ := x["memory_authorization"].(string)
				if !e.memoryResultAuthorized(stamp) {
					return invalidatedMemoryResult(x, "Retained memory authorization changed or is missing; re-run the authorized query.")
				}
			}
			epoch, _ := x["memory_epoch"].(string)
			current, err := e.Store.DataEpoch(ctx)
			if epoch == "" || err != nil || epoch != current {
				delete(x, "text")
				delete(x, "result")
				delete(x, "content")
				x["invalidated"] = true
				x["instruction"] = "Retained memory page changed or cannot be validated; re-run the current memory query, preserving the original action identity."
				return x
			}
		}
		id, _ := x["id"].(string)
		scope, _ := x["scope"].(string)
		if id != "" && scope != "" {
			if _, ok := x["detail"]; ok {
				m, err := e.Store.ReadMemory(ctx, []string{scope}, id)
				old, _ := x["version"].(float64)
				if err != nil || int(old) != m.Version {
					return map[string]any{"id": id, "scope": scope, "invalidated": true, "instruction": "Memory changed or was forgotten; read the current version before using it."}
				}
			}
			if _, ok := x["role"]; ok {
				ev, err := e.Store.ReadEvent(ctx, []string{scope}, id)
				old, _ := x["revision"].(float64)
				if err != nil || int(old) != ev.Revision {
					return map[string]any{"id": id, "scope": scope, "invalidated": true, "instruction": "Source changed or was forgotten; read the current version before using it."}
				}
				e.refreshEvidenceRow(x, ev)
			}
		}
		for key, v := range x {
			x[key] = e.refreshMemoryToolValue(ctx, v)
		}
		return x
	default:
		return value
	}
}

func memoryQueryResult(tool string) bool {
	return tool == "memory_read" || tool == "memory_search" || tool == "agent_global_search"
}

func (e *Engine) memoryResultAuthorized(stamp string) bool {
	provider, ok := e.Tools.(interface{ MemoryResultAuthorized(string) bool })
	return ok && stamp != "" && provider.MemoryResultAuthorized(stamp)
}

func (e *Engine) memoryToolValueAuthorized(value any) bool {
	packet, ok := value.(map[string]any)
	if !ok {
		return false
	}
	stamp, _ := packet["_memory_authorization"].(string)
	return e.memoryResultAuthorized(stamp)
}

func invalidatedMemoryResult(value any, instruction string) map[string]any {
	clean := map[string]any{"invalidated": true, "instruction": instruction}
	if original, ok := value.(map[string]any); ok {
		for _, key := range []string{"result_id", "next_offset", "next_cursor", "next_source_offset", "scope", "id", "tool", "source"} {
			if v, exists := original[key]; exists {
				clean[key] = v
			}
		}
	}
	return clean
}
