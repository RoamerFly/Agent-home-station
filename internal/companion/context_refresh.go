package companion

import (
	"context"
	"encoding/json"
	"time"
)

// A memory tool can forget or correct an entry during a running Agent turn.
// Revalidate dynamic packets before the next model call. Preserve the current
// request, effective followups and tool/result envelopes in full.
func (e *Engine) refreshMemoryPackets(ctx context.Context, turns []map[string]string) []map[string]string {
	if e.Store.db == nil {
		return turns
	}
	for i, turn := range turns {
		if turn["role"] != "user" {
			continue
		}
		var packet map[string]json.RawMessage
		if json.Unmarshal([]byte(turn["content"]), &packet) != nil {
			continue
		}
		var kind string
		_ = json.Unmarshal(packet["kind"], &kind)
		changed := false
		switch kind {
		case "long_term_memory":
			var rows []map[string]any
			if json.Unmarshal(packet["items"], &rows) != nil {
				continue
			}
			kept := []map[string]any{}
			used := 0
			for _, row := range rows {
				id, _ := row["id"].(string)
				scope, _ := row["scope"].(string)
				m, err := e.Store.ReadMemory(ctx, []string{scope}, id)
				if err != nil {
					continue
				}
				text := m.Detail
				if m.Representation(time.Now()) != "detail" {
					text = m.Summary
				}
				row["text"] = text
				row["status"] = m.Status
				row["version"] = m.Version
				b, _ := json.Marshal(row)
				cost := EstimateTokens(string(b))
				if used+cost > e.memorySettings().RecallTokens {
					continue
				}
				used += cost
				kept = append(kept, row)
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
					if _, err := e.Store.ReadEvent(ctx, []string{scope}, id); err != nil {
						continue
					}
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
				if _, err := e.Store.ReadEvent(ctx, []string{scope}, id); err != nil {
					turns[i] = nil
				}
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
