package companion

import (
	"encoding/json"
	"sort"
	"time"
)

// Selection is newest-first so LIMIT selects the nearest predecessors. Model
// presentation is oldest-first; never change the query cursor to achieve it.
func historyEventLess(a, b MemoryEvent) bool {
	at, bt := a.At, b.At
	if a.Source != nil && a.Source.OccurredAt != "" {
		at = a.Source.OccurredAt
	}
	if b.Source != nil && b.Source.OccurredAt != "" {
		bt = b.Source.OccurredAt
	}
	ta, ea := time.Parse(time.RFC3339Nano, at)
	tb, eb := time.Parse(time.RFC3339Nano, bt)
	if (ea == nil) != (eb == nil) {
		return ea == nil
	}
	if ea == nil && eb == nil && !ta.Equal(tb) {
		return ta.Before(tb)
	}
	// Missing times stay explicitly unknown. A persistent sequence orders ties;
	// opaque native IDs and tool completion times are not event timestamps.
	seq := func(e MemoryEvent) int64 {
		if e.Source != nil && e.Source.Sequence != 0 {
			return e.Source.Sequence
		}
		return e.Seq
	}
	if seq(a) != seq(b) {
		return seq(a) < seq(b)
	}
	return a.ID < b.ID
}

func (h *historyMaterialNeed) directoryForModel(result string) string {
	var p MemoryPage
	if json.Unmarshal([]byte(result), &p) != nil {
		return result
	}
	sort.SliceStable(p.Items, func(i, j int) bool {
		if p.Items[i].Event == nil || p.Items[j].Event == nil {
			return p.Items[i].Event != nil && p.Items[j].Event == nil
		}
		return historyEventLess(*p.Items[i].Event, *p.Items[j].Event)
	})
	// Preserve every pagination/coverage field, including future additions.
	var raw map[string]any
	if json.Unmarshal([]byte(result), &raw) != nil {
		return result
	}
	raw["items"], raw["selection_order"], raw["presentation_order"] = p.Items, "time_desc", "time_asc"
	b, _ := json.Marshal(raw)
	return string(b)
}

func (h *historyMaterialNeed) rememberReadPacket(name string, args json.RawMessage, result, payload string) {
	if h == nil || name != "memory_read" && name != "video_inspect" {
		return
	}
	var q struct {
		ID      string `json:"id"`
		Message string `json:"message_id"`
		Action  string `json:"action"`
		Part    string `json:"part"`
	}
	if json.Unmarshal(args, &q) != nil {
		return
	}
	var r struct {
		Message string `json:"source_message_id"`
		Source  string `json:"source"`
		Event   struct {
			ID string `json:"id"`
		} `json:"event"`
	}
	if json.Unmarshal([]byte(result), &r) != nil {
		return
	}
	id := ""
	if name == "memory_read" && (q.Part == "observation" || q.Part == "transcript") {
		id = r.Event.ID
		if id == "" && h.IDs[q.ID] {
			id = q.ID
		}
		if id == "" {
			id = h.MessageIDs[q.Message]
		}
	} else if name == "video_inspect" && (q.Action == "audio" || q.Action == "frames") {
		id = h.MessageIDs[r.Message]
		if id == "" {
			id = h.MessageIDs[r.Source]
		}
		if id == "" {
			id = h.MessageIDs[q.Message]
		}
	}
	if !h.IDs[id] {
		return
	}
	if h.ReadPackets == nil {
		h.ReadPackets = map[string]string{}
	}
	h.ReadPackets[payload] = id
}

func (h *historyMaterialNeed) tagReadPacket(name string, args json.RawMessage, result, payload string) string {
	if h == nil {
		return payload
	}
	h.rememberReadPacket(name, args, result, payload)
	id := h.ReadPackets[payload]
	if id == "" {
		return payload
	}
	var packet map[string]any
	if json.Unmarshal([]byte(payload), &packet) != nil {
		return payload
	}
	// Memory revalidation reserializes results before each call. Keep a host
	// provenance tag so chronology does not depend on byte-identical formatting.
	packet["history_material_event_id"] = id
	raw, _ := json.Marshal(packet)
	return string(raw)
}

// Do not reorder assistant/function-result pairs. Move only their source data
// into one chronological packet for this model call, keeping the execution
// receipts and full-result continuation handles in their original positions.
// The durable transcript is untouched, including when a follow-up changes scope.
func (h *historyMaterialNeed) contextForModel(turns []map[string]string) []map[string]string {
	// Before the model has selected and received a directory, this helper must
	// be a no-op. In particular, never hide the current request or its quoted
	// anchor merely because it may later become a history locator.
	if h == nil || len(h.Events) == 0 {
		return turns
	}
	out := make([]map[string]string, 0, len(turns)+1)
	contents := map[string][]any{}
	for _, turn := range turns {
		var data map[string]any
		if json.Unmarshal([]byte(turn["content"]), &data) != nil {
			out = append(out, turn)
			continue
		}
		id := h.ReadPackets[turn["content"]]
		sourceKind, _ := data["source"].(string)
		if tagged, _ := data["history_material_event_id"].(string); h.IDs[tagged] && sourceKind == "untrusted_tool_result" {
			id = tagged
		}
		if id != "" {
			packet := make(map[string]any, len(data))
			for key, value := range data {
				packet[key] = value
			}
			contents[id] = append(contents[id], packet)
			data["result"] = map[string]any{"source_event_id": id, "content_location": "chronological_history_materials"}
		} else {
			h.prepareHistoryData(data, contents)
		}
		copy := make(map[string]string, len(turn))
		for key, value := range turn {
			copy[key] = value
		}
		raw, _ := json.Marshal(data)
		copy["content"] = string(raw)
		out = append(out, copy)
	}
	events := make([]MemoryEvent, 0, len(h.Events))
	for _, e := range h.Events {
		events = append(events, e)
	}
	sort.SliceStable(events, func(i, j int) bool { return historyEventLess(events[i], events[j]) })
	rows := make([]map[string]any, 0, len(events))
	for i, e := range events {
		rows = append(rows, map[string]any{"position": i + 1, "event_id": e.ID, "message_id": e.Trace, "at": e.At, "source": e.Source, "materials": contents[e.ID], "read_verified": h.ReadIDs[e.ID]})
	}
	raw, _ := json.Marshal(map[string]any{"kind": "chronological_history_materials", "source": "untrusted_source_materials", "presentation_order": "time_asc", "anchor_message_id": h.Anchor, "anchor_role": "locator_only_excluded", "requested_count": h.Count, "selected_count": len(rows), "items": rows})
	out = append(out, map[string]string{"role": "user", "content": string(raw)})
	return out
}

func (h *historyMaterialNeed) prepareHistoryData(data map[string]any, contents map[string][]any) {
	kind, _ := data["kind"].(string)
	if kind == "current_message" {
		if text, ok := data["text"].(string); ok {
			data["text"] = UserRequestText(text)
		}
		delete(data, "recent_media_focus")
		data["history_anchor_role"] = "locator_only_excluded"
		if runtime, ok := data["runtime_context"].(map[string]any); ok {
			if text, ok := runtime["text"].(string); ok {
				runtime["text"] = UserRequestText(text)
			}
		}
	}
	source, _ := data["source"].(map[string]any)
	message, _ := data["message_id"].(string)
	if message == "" {
		message, _ = source["message_id"].(string)
	}
	// A fallback anchor may be the current message's own source. The current
	// request remains authoritative and cannot be reduced to a locator.
	if message == h.Anchor && kind != "current_message" {
		delete(data, "media_observations")
		delete(data, "text")
		data["history_anchor_role"] = "locator_only_excluded"
	}
	if observations, ok := data["media_observations"].([]any); ok {
		kept := []any{}
		for _, obs := range observations {
			row, _ := obs.(map[string]any)
			src, _ := row["source"].(map[string]any)
			id, _ := src["event_id"].(string)
			mid, _ := src["message_id"].(string)
			// A quoted anchor is excluded even when carried beside the current
			// request. Preserve it only for the source-fallback case where the
			// current message itself was (necessarily) used as the locator.
			if mid == h.Anchor && !(kind == "current_message" && message == h.Anchor) {
				continue
			}
			if h.IDs[id] {
				contents[id] = append(contents[id], obs)
			} else {
				kept = append(kept, obs)
			}
		}
		data["media_observations"] = kept
	}
	for key, value := range data {
		// SourceRef is immutable identity metadata, not a content container.
		if key == "source" || key == "quoted_source" {
			continue
		}
		if child, ok := value.(map[string]any); ok {
			h.prepareHistoryData(child, contents)
		}
		if rows, ok := value.([]any); ok {
			for _, row := range rows {
				if child, ok := row.(map[string]any); ok {
					h.prepareHistoryData(child, contents)
				}
			}
		}
	}
}
