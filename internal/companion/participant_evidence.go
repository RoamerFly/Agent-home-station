package companion

import (
	"context"
	"encoding/json"
	"time"
)

// Only literal human words enter this first-person binding table. Upload and
// runtime actors remain available in their own envelopes without implying a
// speaker for a recording or for machine-produced observation text.
func (e *Engine) participantUtterances(in Input, scope Scope, packets []map[string]string) []map[string]any {
	selected := map[string]bool{}
	for _, p := range packets {
		var v struct {
			ID       string `json:"source_event_id"`
			Timeline []struct {
				ID string `json:"source_event_id"`
			} `json:"timeline"`
		}
		if json.Unmarshal([]byte(p["content"]), &v) == nil {
			if v.ID != "" {
				selected[v.ID] = true
			}
			for _, r := range v.Timeline {
				if r.ID != "" {
					selected[r.ID] = true
				}
			}
		}
	}
	out := []map[string]any{}
	for _, t := range groundedHistory(scope, in) {
		if t.Role != "user" || !selected[t.EventID] {
			continue
		}
		t = e.evidenceTurn(context.Background(), scope.ID, t)
		literal, state := literalTurn(t)
		if t.Sender == "" || !reliableLiteral(literal, state) {
			continue
		}
		row := map[string]any{"speaker_id": t.Sender, "message_id": t.TraceID, "event_id": t.EventID, "utterance": boundedText(literal, 1200), "user_text_state": state, "evidence_kind": turnEvidenceKind(t), "incoming_I_means": t.Sender, "incoming_you_means": t.Addressees, "quote_chain": quoteRoles(t.Sender, t.Source, t.QuotedSource)}
		if !t.Time.IsZero() {
			row["utterance_local_time"] = t.Time.In(location(e.Config.Timezone)).Format(time.RFC3339Nano)
			row["utterance_age_seconds"] = time.Since(t.Time).Seconds()
		}
		out = append(out, row)
	}
	words, state := e.inputLiteral(in)
	if runtimeInput(in) || in.Sender == "" || !reliableLiteral(words, state) {
		return out
	}
	row := map[string]any{"speaker_id": in.Sender, "message_id": in.TraceID, "utterance": words, "user_text_state": state, "incoming_I_means": in.Sender, "incoming_you_means": in.Addressees, "quote_chain": quoteRoles(in.Sender, in.Source, in.QuotedSource)}
	at := in.MessageTime
	if at == "" && in.Source != nil {
		at = in.Source.OccurredAt
	}
	if stamp, err := time.Parse(time.RFC3339Nano, at); err == nil {
		row["utterance_local_time"] = stamp.In(location(e.Config.Timezone)).Format(time.RFC3339Nano)
		row["utterance_age_seconds"] = time.Since(stamp).Seconds()
	}
	return append(out, row)
}
