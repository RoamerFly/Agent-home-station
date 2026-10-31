package companion

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// A review row is an immutable, typed view of already-authorized evidence. Its
// source revisions remain host-bound; the model echoes only a short row ref and
// an exact quotation. An exact quote proves presence, not semantic entailment.
type GroundingEvidenceRow struct {
	Ref            string         `json:"ref"`
	Kind           string         `json:"kind"`
	Text           string         `json:"text"`
	SourceRefs     []string       `json:"source_refs"`
	SourceVersions map[string]int `json:"source_versions"`
	Metadata       map[string]any `json:"metadata"`
}

type groundingTableBuilder struct {
	rows       []GroundingEvidenceRow
	positions  map[string]int
	messages   map[string]string
	versions   map[string]int
	allowed    map[string]bool
	currentIDs []string
}

func projectGroundingCheckState(state map[string]any) (*GroundingReviewBinding, []string, error) {
	binding, aliases, err := projectGroundingState(state)
	if err != nil {
		return nil, nil, err
	}
	var projected map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(binding.ReviewInput)))
	decoder.UseNumber()
	if decoder.Decode(&projected) != nil {
		return nil, nil, errors.New("reply_grounding_projection_invalid")
	}
	b := groundingTableBuilder{positions: map[string]int{}, messages: map[string]string{}, versions: map[string]int{}, allowed: map[string]bool{}}
	for _, alias := range aliases {
		b.allowed[alias] = true
	}
	if err = indexGroundingSources(projected, b.messages, b.versions); err != nil {
		return nil, nil, err
	}
	// History is indexed before current so the same transport message resolves
	// to its persistent alias even if Input.Source has not copied EventID yet.
	for _, packet := range groundingMaps(projected["sources"]) {
		if row, ok := packet["content"].(map[string]any); ok {
			if err = b.message(row); err != nil {
				return nil, nil, err
			}
		}
	}
	if current, ok := projected["current"].(map[string]any); ok {
		before := len(b.rows)
		if err = b.message(current); err != nil {
			return nil, nil, err
		}
		for _, row := range b.rows[before:] {
			b.currentIDs = append(b.currentIDs, row.Ref)
		}
		if ref := b.messageRef(current); ref != "" {
			for _, row := range b.rows {
				for _, source := range row.SourceRefs {
					if source == ref {
						b.currentIDs = append(b.currentIDs, row.Ref)
					}
				}
			}
		}
	}
	for _, packet := range groundingMaps(projected["recalled_evidence"]) {
		memory, ok := packet["content"].(map[string]any)
		if !ok {
			content, _ := packet["content"].(string)
			if json.Unmarshal([]byte(content), &memory) != nil {
				continue
			}
		}
		for _, item := range groundingMaps(memory["items"]) {
			// This packet is an explicitly generated memory envelope, not user
			// text. Its structural source references alone may be aliased.
			item = projectGroundingMetadata(item, "", reverseGroundingAliases(binding.SourceAliases)).(map[string]any)
			refs := b.refs(groundingStrings(item["source_event_ids"]))
			metadata := map[string]any{}
			for key, value := range item {
				if key != "text" {
					metadata[key] = value
				}
			}
			if err = b.add("derived_memory", groundingString(item["text"]), refs, metadata, "memory:"+groundingString(item["id"])); err != nil {
				return nil, nil, err
			}
		}
	}
	for _, execution := range groundingMaps(projected["execution_evidence"]) {
		ref := groundingString(execution["evidence_id"])
		raw, _ := canonicalGroundingJSON(execution)
		if err = b.add("execution_result", string(raw), b.refs([]string{ref}), map[string]any{"evidence_kind": "observed_tool_execution"}, "execution:"+ref); err != nil {
			return nil, nil, err
		}
	}
	for _, module := range []struct{ field, kind string }{
		{"request_knowledge_state", "host_knowledge"},
		{"source_reference_contracts", "source_contract"},
		{"current_role_evidence", "role_contract"},
		{"temporal_evidence", "temporal_contract"},
		{"quote_role_bindings", "quote_contract"},
	} {
		value := projected[module.field]
		if module.field == "temporal_evidence" {
			if temporal, ok := value.(map[string]any); !ok || len(groundingMaps(temporal["candidates"])) == 0 {
				continue
			}
		}
		if module.field == "request_knowledge_state" && len(groundingMaps(projected["execution_evidence"])) == 0 {
			current, _ := projected["current"].(map[string]any)
			if current["mode"] == "social" && groundingString(current["task_id"]) == "" && current["background_result"] != true {
				continue
			}
		}
		if !groundingMaterial(value) {
			continue
		}
		raw, _ := canonicalGroundingJSON(value)
		refs := []string{}
		if module.field == "request_knowledge_state" {
			refs = b.refs([]string{"K1"})
		}
		if err = b.add(module.kind, string(raw), refs, map[string]any{"producer": "host", "coverage": "bounded_current_request", "module": module.field}, module.field); err != nil {
			return nil, nil, err
		}
	}
	var candidate Decision
	rawCandidate, _ := json.Marshal(projected["candidate"])
	if json.Unmarshal(rawCandidate, &candidate) != nil {
		return nil, nil, errors.New("reply_grounding_projection_invalid")
	}
	units := groundingReviewUnits(candidate.Messages)
	wire := map[string]any{"candidate": projected["candidate"], "review_units": units, "evidence_table": b.rows, "outgoing_audiences": projected["outgoing_audiences"], "current_evidence_refs": uniqueSortedEvidence(b.currentIDs), "evidence_scope": projected["evidence_scope"], "current_time": projected["current_time"], "timezone": projected["timezone"], "privacy_review": projected["privacy_review"] == true}
	if voice := groundingString(projected["expected_voice_id"]); voice != "" {
		wire["expected_voice_id"] = voice
	}
	if profile := groundingString(projected["expected_voice_profile"]); profile != "" {
		wire["expected_voice_profile"] = profile
	}
	input, err := canonicalGroundingJSON(wire)
	if err != nil {
		return nil, nil, err
	}
	binding.ReviewInput, binding.EvidenceTable, binding.ReviewUnits = input, b.rows, units
	return binding, aliases, nil
}

func (b *groundingTableBuilder) message(row map[string]any) error {
	for _, key := range []string{"timeline", "unread_messages", "events"} {
		for _, nested := range groundingMaps(row[key]) {
			if err := b.message(nested); err != nil {
				return err
			}
		}
	}
	if quoted, ok := row["quoted_message_evidence"].(map[string]any); ok {
		if err := b.message(quoted); err != nil {
			return err
		}
	}
	ref := b.messageRef(row)
	refs := b.refs([]string{ref})
	metadata := map[string]any{}
	for _, key := range []string{"source", "scope", "account", "thread", "channel", "message_id", "at", "message_time", "speaker_id", "speaker", "message_sender_id", "message_sender_name", "request_actor_id", "addressees", "quoted_source", "quote_chain", "user_text_state", "text_excerpted", "temporal_precision", "personal_context_redacted", "uploader_id", "voice_speaker_id", "voice_speaker_state"} {
		if value, ok := row[key]; ok {
			metadata[key] = value
		}
	}
	text := groundingString(row["text"])
	kind := "user_utterance"
	switch groundingString(row["evidence_kind"]) {
	case "assistant_statement":
		kind = "assistant_statement"
	case "runtime_context", "source_user_request":
		kind = "source_user_request"
	case "unverified_event_reference":
		kind = "unverified_source"
	}
	if row["role"] == "assistant" {
		kind = "assistant_statement"
	}
	if kind == "user_utterance" && !reliableLiteral(text, groundingString(row["user_text_state"])) {
		kind = "unverified_source"
	}
	if text != "" {
		if err := b.add(kind, text, refs, metadata, b.messageIdentity(row, ref)); err != nil {
			return err
		}
	}
	if groundingString(row["evidence_kind"]) == "media_upload_event" {
		metadata["evidence_kind"] = "media_upload_event"
		if err := b.add("media_upload", "", refs, metadata, b.messageIdentity(row, ref)); err != nil {
			return err
		}
	}
	for _, observation := range groundingMaps(row["media_observations"]) {
		mediaRef := b.messageRef(observation)
		if mediaRef == "" {
			mediaRef = ref
		}
		meta := map[string]any{}
		for key, value := range observation {
			if key != "observation" {
				meta[key] = value
			}
		}
		identity := strings.Join([]string{b.messageIdentity(observation, mediaRef), groundingString(observation["asset_id"]), fmt.Sprint(observation["observation_version"]), groundingString(observation["observed_range"])}, "\x00")
		if b.messageIdentity(observation, mediaRef) == "" {
			identity = ""
		}
		if err := b.add("media_observation", groundingString(observation["observation"]), b.refs([]string{mediaRef}), meta, identity); err != nil {
			return err
		}
	}
	if runtime, ok := row["runtime_context"].(map[string]any); ok {
		meta := map[string]any{}
		for key, value := range runtime {
			if key != "text" {
				meta[key] = value
			}
		}
		if err := b.add("runtime_context", groundingString(runtime["text"]), refs, meta, b.messageIdentity(row, ref)); err != nil {
			return err
		}
	}
	if summary := row["summary"]; groundingMaterial(summary) {
		raw, _ := canonicalGroundingJSON(summary)
		if err := b.add("derived_summary", string(raw), nil, map[string]any{"can_support_user_facts_without_sources": false}, "summary"); err != nil {
			return err
		}
	}
	return nil
}

func (b *groundingTableBuilder) add(kind, text string, refs []string, metadata map[string]any, identity string) error {
	if refs == nil {
		refs = []string{}
	}
	versions := map[string]int{}
	for _, ref := range refs {
		if version := b.versions[ref]; version > 0 {
			versions[ref] = version
		}
	}
	if identity == "" {
		// Identical words without stable source identity are distinct evidence
		// occurrences; never infer event equality from body text alone.
		identity = fmt.Sprintf("anonymous:%d", len(b.rows))
	}
	key := kind + "\x00" + identity
	if position, ok := b.positions[key]; ok {
		old := &b.rows[position]
		oldExcerpted, _ := old.Metadata["text_excerpted"].(bool)
		newExcerpted, _ := metadata["text_excerpted"].(bool)
		if groundingIdentityConflict(old.Metadata, metadata) {
			return errors.New("reply_grounding_evidence_identity_conflict")
		}
		if old.Text != text {
			// Full current text may extend a history excerpt, never replace it
			// with unrelated wording under the same source identity.
			if oldExcerpted && !newExcerpted && strings.Contains(text, old.Text) {
				old.Text = text
				old.Metadata["text_excerpted"] = false
			} else if !newExcerpted || oldExcerpted || !strings.Contains(old.Text, text) {
				return errors.New("reply_grounding_evidence_projection_conflict")
			}
		}
		for field, value := range metadata {
			if prior, exists := old.Metadata[field]; !exists || !groundingMaterial(prior) {
				old.Metadata[field] = value
			}
		}
		if old.Metadata["text_excerpted"] != nil || metadata["text_excerpted"] != nil {
			old.Metadata["text_excerpted"] = oldExcerpted && newExcerpted
		}
		return nil
	}
	b.positions[key] = len(b.rows)
	b.rows = append(b.rows, GroundingEvidenceRow{Ref: fmt.Sprintf("E%d", len(b.rows)+1), Kind: kind, Text: text, SourceRefs: refs, SourceVersions: versions, Metadata: metadata})
	return nil
}

func (b *groundingTableBuilder) messageRef(row map[string]any) string {
	for _, key := range []string{"source_event_id", "event_id", "evidence_id"} {
		if ref := groundingString(row[key]); b.allowed[ref] {
			return ref
		}
	}
	if source, ok := row["source"].(map[string]any); ok && (groundingString(source["message_id"]) != "" || groundingString(source["event_id"]) != "") {
		if ref := groundingString(source["event_id"]); b.allowed[ref] {
			return ref
		}
		if ref := b.messages[groundingMessageKey(source)]; ref != "" {
			return ref
		}
	}
	return ""
}

func groundingIdentityConflict(a, b map[string]any) bool {
	for _, field := range []string{"scope", "account", "thread", "channel", "speaker_id", "message_sender_id", "request_actor_id", "uploader_id", "voice_speaker_id", "transcript_first_person_id", "creator_id", "owner_id", "addressees", "observation_version", "observed_range", "observation_state"} {
		left, right := a[field], b[field]
		if !groundingMaterial(left) || !groundingMaterial(right) {
			continue
		}
		if field == "addressees" {
			left, right = groundingStrings(left), groundingStrings(right)
		}
		l, _ := canonicalGroundingJSON(left)
		r, _ := canonicalGroundingJSON(right)
		if string(l) != string(r) {
			return true
		}
	}
	left, _ := a["source"].(map[string]any)
	right, _ := b["source"].(map[string]any)
	for _, field := range []string{"event_id", "instance_id", "account", "thread", "message_id", "server_id", "sender", "revision", "occurred_at"} {
		l, r := left[field], right[field]
		if !groundingMaterial(l) || !groundingMaterial(r) {
			continue
		}
		if field == "occurred_at" {
			lt, le := time.Parse(time.RFC3339Nano, groundingString(l))
			rt, re := time.Parse(time.RFC3339Nano, groundingString(r))
			if le == nil && re == nil && lt.Equal(rt) {
				continue
			}
		}
		lb, _ := canonicalGroundingJSON(l)
		rb, _ := canonicalGroundingJSON(r)
		if string(lb) != string(rb) {
			return true
		}
	}
	return false
}

func (b *groundingTableBuilder) messageIdentity(row map[string]any, ref string) string {
	if ref != "" {
		return ref
	}
	if source, ok := row["source"].(map[string]any); ok {
		if groundingString(source["instance_id"]) != "" && groundingString(source["message_id"]) != "" && groundingString(source["account"]) != "" && groundingString(source["thread"]) != "" && groundingString(source["sender"]) != "" && (groundingString(source["server_id"]) != "" || groundingString(source["occurred_at"]) != "") {
			body, _ := canonicalGroundingJSON(source)
			return string(body)
		}
	}
	if id, scope, author, at := groundingString(row["message_id"]), groundingString(row["scope"]), groundingString(row["message_sender_id"]), groundingString(row["at"]); id != "" && scope != "" && author != "" && at != "" {
		return strings.Join([]string{scope, author, id, at}, "\x00")
	}
	return ""
}

func (b *groundingTableBuilder) refs(values []string) []string {
	out := []string{}
	for _, ref := range values {
		if b.allowed[ref] {
			out = append(out, ref)
		}
	}
	return uniqueSortedEvidence(out)
}

func indexGroundingSources(value any, messages map[string]string, versions map[string]int) error {
	recordVersion := func(ref string, value int) error {
		if ref == "" || value <= 0 {
			return nil
		}
		if prior := versions[ref]; prior > 0 && prior != value {
			return errors.New("reply_grounding_evidence_version_conflict")
		}
		versions[ref] = value
		return nil
	}
	switch v := value.(type) {
	case map[string]any:
		if source, ok := v["source"].(map[string]any); ok {
			ref, message := groundingString(source["event_id"]), groundingString(source["message_id"])
			if ref != "" && message != "" {
				key := groundingMessageKey(source)
				if prior := messages[key]; prior != "" && prior != ref {
					return errors.New("reply_grounding_evidence_identity_conflict")
				}
				messages[key] = ref
				if err := recordVersion(ref, groundingInt(source["revision"])); err != nil {
					return err
				}
			}
		}
		if sourceVersions, ok := v["source_versions"].(map[string]any); ok {
			for ref, version := range sourceVersions {
				if err := recordVersion(ref, groundingInt(version)); err != nil {
					return err
				}
			}
		}
		for key, item := range v {
			if key != "arguments" && key != "result" {
				if err := indexGroundingSources(item, messages, versions); err != nil {
					return err
				}
			}
		}
	case []any:
		for _, item := range v {
			if err := indexGroundingSources(item, messages, versions); err != nil {
				return err
			}
		}
	}
	return nil
}

func reverseGroundingAliases(reverse map[string]string) map[string]string {
	forward := map[string]string{}
	for alias, id := range reverse {
		forward[id] = alias
	}
	return forward
}
func groundingMessageKey(source map[string]any) string {
	if groundingString(source["message_id"]) == "" {
		return ""
	}
	parts := []string{}
	for _, key := range []string{"instance_id", "account", "thread", "message_id", "server_id", "sender"} {
		parts = append(parts, groundingString(source[key]))
	}
	return strings.Join(parts, "\x00")
}
func groundingString(value any) string { text, _ := value.(string); return text }
func groundingInt(value any) int {
	switch v := value.(type) {
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}
func groundingMaps(value any) []map[string]any {
	out := []map[string]any{}
	if rows, ok := value.([]map[string]any); ok {
		return rows
	}
	if rows, ok := value.([]any); ok {
		for _, item := range rows {
			if row, ok := item.(map[string]any); ok {
				out = append(out, row)
			}
		}
	}
	return out
}
func groundingStrings(value any) []string {
	out := []string{}
	if values, ok := value.([]string); ok {
		out = append(out, values...)
	}
	if values, ok := value.([]any); ok {
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
	}
	sort.Strings(out)
	return out
}
func groundingMaterial(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case []any:
		return len(v) > 0
	case map[string]any:
		for key, item := range v {
			if key == "interpretation" || key == "instruction" || key == "note" {
				continue
			}
			if groundingMaterial(item) {
				return true
			}
		}
		return false
	case string:
		return v != ""
	case bool:
		return v
	case json.Number:
		return v != "0"
	case float64:
		return v != 0
	case int:
		return v != 0
	}
	return true
}
