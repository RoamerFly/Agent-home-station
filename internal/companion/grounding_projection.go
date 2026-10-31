package companion

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// GroundingReviewBinding is host-only audit material. None of the reverse
// aliases or original hashed source IDs are sent back to the reviewer.
type GroundingReviewBinding struct {
	SourceAliases map[string]string      `json:"source_aliases"`
	OriginalState json.RawMessage        `json:"original_state"`
	ReviewInput   json.RawMessage        `json:"review_input"`
	EvidenceTable []GroundingEvidenceRow `json:"evidence_table,omitempty"`
	ReviewUnits   []GroundingReviewUnit  `json:"review_units,omitempty"`
}

func canonicalGroundingJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

func projectGroundingState(state map[string]any) (*GroundingReviewBinding, []string, error) {
	original, err := canonicalGroundingJSON(state)
	if err != nil {
		return nil, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(original))
	decoder.UseNumber()
	var normalized map[string]any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, nil, err
	}
	ids := []string{}
	seen := map[string]bool{}
	values, _ := normalized["evidence_event_ids"].([]any)
	for _, value := range values {
		if id, ok := value.(string); ok && id != "" && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	sort.Strings(ids)
	forward, reverse := map[string]string{}, map[string]string{}
	wireRefs := []string{}
	for _, id := range ids {
		alias := fmt.Sprintf("S%d", len(reverse)+1)
		forward[id], reverse[alias] = alias, id
		wireRefs = append(wireRefs, alias)
	}
	// Knowledge references are derived host metadata, never persistent sources.
	if knowledge, ok := normalized["request_knowledge_state"].(map[string]any); ok {
		if ref, _ := knowledge["ref"].(string); ref != "" {
			reverse[ref] = ref
			wireRefs = append(wireRefs, ref)
		}
	}
	projected := projectGroundingMetadata(normalized, "", forward)
	projected.(map[string]any)["evidence_event_ids"] = wireRefs
	input, err := canonicalGroundingJSON(projected)
	if err != nil {
		return nil, nil, err
	}
	return &GroundingReviewBinding{SourceAliases: reverse, OriginalState: original, ReviewInput: input}, wireRefs, nil
}

// Only fields with a defined structural meaning may be projected. User text,
// observations, quotations, model messages and opaque tool arguments/results
// are not searched/replaced, even when they contain the exact source hash.
func projectGroundingMetadata(value any, key string, aliases map[string]string) any {
	if key == "result" || key == "arguments" {
		return value
	}
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		if key == "source_versions" {
			for id, version := range v {
				if alias, ok := aliases[id]; ok {
					id = alias
				}
				out[id] = version
			}
			return out
		}
		for field, item := range v {
			switch field {
			case "context_version", "body_hash", "raw_body_hash", "evidence_epoch", "evidence_authorization", "data_snapshot_epoch":
				continue // Still present in OriginalState and the host digest.
			}
			if field == "content" && (v["role"] == "user" || v["role"] == "assistant") {
				if text, ok := item.(string); ok {
					var packet map[string]any
					if json.Unmarshal([]byte(text), &packet) == nil && packet["kind"] != nil {
						// Structured prompt envelopes become ordinary JSON objects;
						// literal text inside the packet remains byte-for-byte intact.
						out[field] = projectGroundingMetadata(packet, "", aliases)
						continue
					}
				}
			}
			out[field] = projectGroundingMetadata(item, field, aliases)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = projectGroundingMetadata(v[i], key, aliases)
		}
		return out
	case string:
		switch key {
		case "event_id", "source_event_id", "evidence_id", "anchor_event_id", "quoted_event_id", "quoting_event_id", "source_event_ids", "media_source_ids", "evidence_event_ids", "accepted_receipt_refs", "based_on_refs", "source_refs":
			if alias, ok := aliases[v]; ok {
				return alias
			}
		}
	}
	return value
}

func restoreGroundingSourceRefs(verdict GroundingVerdict, binding *GroundingReviewBinding) (GroundingVerdict, error) {
	for i := range verdict.Bubbles {
		for j := range verdict.Bubbles[i].Violations {
			refs := verdict.Bubbles[i].Violations[j].SourceRefs
			for k, alias := range refs {
				id, ok := binding.SourceAliases[alias]
				if !ok {
					return GroundingVerdict{}, fmt.Errorf("reply_grounding_verdict_invalid")
				}
				refs[k] = id
			}
		}
	}
	return verdict, nil
}

// An editor cannot promote execution receipts or derived host knowledge into
// persistent human sources. Actual asset IDs still use the media validator.
func restoreGroundingReplyContexts(contexts []ReplyContext, binding *GroundingReviewBinding) ([]ReplyContext, error) {
	decode := func(alias string) (string, error) {
		id, ok := binding.SourceAliases[alias]
		if !ok || id == "K1" || len(id) >= 10 && id[:10] == "execution:" {
			return "", fmt.Errorf("reply_grounding_repair_source_invalid")
		}
		return id, nil
	}
	out := make([]ReplyContext, len(contexts))
	for i := range contexts {
		out[i] = *cloneReplyContext(&contexts[i])
		for j, ref := range out[i].SourceEventIDs {
			id, err := decode(ref)
			if err != nil {
				return nil, err
			}
			out[i].SourceEventIDs[j] = id
		}
		if out[i].AnchorEventID != "" {
			id, err := decode(out[i].AnchorEventID)
			if err != nil {
				return nil, err
			}
			out[i].AnchorEventID = id
		}
		versions := map[string]int{}
		for ref, version := range out[i].SourceVersions {
			id, err := decode(ref)
			if err != nil {
				return nil, err
			}
			versions[id] = version
		}
		out[i].SourceVersions = versions
		for j, ref := range out[i].MediaSourceIDs {
			if id, ok := binding.SourceAliases[ref]; ok {
				if id == "K1" || len(id) >= 10 && id[:10] == "execution:" {
					return nil, fmt.Errorf("reply_grounding_repair_source_invalid")
				}
				out[i].MediaSourceIDs[j] = id
				continue
			}
			for _, real := range binding.SourceAliases {
				if ref == real {
					return nil, fmt.Errorf("reply_grounding_repair_source_invalid")
				}
			}
		}
	}
	return out, nil
}
