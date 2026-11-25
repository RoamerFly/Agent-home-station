package companion

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// Summary is data, never policy. Typed fields prevent an apparently valid JSON
// scalar or arbitrary tool output from becoming the sole surviving memory.
type contextSummary struct {
	Sources      []SourceRef     `json:"sources,omitempty"`
	Version      int             `json:"version"`
	People       []summaryPerson `json:"people"`
	Topics       []summaryTopic  `json:"topics"`
	CurrentFocus string          `json:"current_focus"`
	Media        []summaryMedia  `json:"media"`
	PendingTasks []summaryTask   `json:"pending_tasks"`
	Preferences  []string        `json:"preferences"`
	Corrections  []string        `json:"corrections"`
}
type summaryPerson struct {
	SourceEventIDs []string `json:"source_event_ids,omitempty"`
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Notes          string   `json:"notes"`
}
type summaryTopic struct {
	SourceEventIDs []string `json:"source_event_ids,omitempty"`
	Text           string   `json:"text"`
	State          string   `json:"state"`
	At             string   `json:"at"`
}
type summaryMedia struct {
	SourceEventIDs []string `json:"source_event_ids,omitempty"`
	Title          string   `json:"title"`
	URL            string   `json:"url"`
	AssetID        string   `json:"asset_id"`
	State          string   `json:"state"`
	At             string   `json:"at"`
}
type summaryTask struct {
	SourceEventIDs []string `json:"source_event_ids,omitempty"`
	ID             string   `json:"id"`
	Request        string   `json:"request"`
	State          string   `json:"state"`
}

const summaryPrompt = `合并旧摘要与新增消息，只输出以下JSON；字段齐全，空集合用[]，version为1：
{"version":1,"people":[{"id":"原ID","name":"姓名","notes":"关系备注"}],"topics":[{"text":"话题","state":"active或ended或corrected","at":"原时间"}],"current_focus":"最近正在讨论的事情","media":[{"title":"原标题","url":"实际URL或空字符串","asset_id":"实际素材ID或空字符串","state":"真实观察/交付状态","at":"原时间"}],"pending_tasks":[{"id":"实际ID或空字符串","request":"谁提出的什么要求","state":"实际状态"}],"preferences":["偏好"],"corrections":["谁对哪条旧信息作了何种更正"]}
people/topics/media/pending_tasks条目均加source_event_ids，保留真实event_id。分清text/literal_user_text原话、media_observations机器观察、assistant旧话和runtime_context背景；引用者quoted_by_id与作者quoted_author_id分别记录。标出更正、已结束任务及媒体来源、ID、状态。topics按时间排序，优先保留新增消息；每数组最多20项，总计不超过3500字符。`

func parseContextSummary(raw string, requireVersion bool) (contextSummary, error) {
	var out contextSummary
	if len(raw) > 256<<10 || !utf8.ValidString(raw) {
		return out, errors.New("summary_response_encoding_or_size_invalid")
	}
	d := json.NewDecoder(bytes.NewBufferString(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&out); err != nil {
		return out, fmt.Errorf("summary_schema: %w", err)
	}
	if d.Decode(new(any)) != io.EOF || out.Version != 1 && (requireVersion || out.Version != 0) {
		return out, errors.New("summary_requires_one_object_with_version_1")
	}
	if requireVersion && (out.People == nil || out.Topics == nil || out.Media == nil || out.PendingTasks == nil || out.Preferences == nil || out.Corrections == nil) {
		return out, errors.New("summary_arrays_required_use_empty_brackets_instead_of_null")
	}
	if len(out.People) > 20 || len(out.Topics) > 20 || len(out.Media) > 20 || len(out.PendingTasks) > 20 || len(out.Preferences) > 20 || len(out.Corrections) > 20 {
		return out, errors.New("summary_each_array_must_have_at_most_20_items")
	}
	// Measure the canonical object, not indentation or JSON unicode escape
	// spelling. Equivalent valid summaries must have the same length result.
	canonical, _ := json.Marshal(out)
	if chars := utf8.RuneCount(canonical); len(canonical) > 14000 || chars > 3500 {
		return out, fmt.Errorf("summary_canonical_length_%d_exceeds_3500_characters; compact_facts_and_old_ended_topics", chars)
	}
	return out, nil
}

// parseCompressionSummary accepts the model's ordinary JSON object while
// normalizing harmless omissions (missing version/empty arrays) before the
// host attaches verified same-scope provenance. Strict schema checks remain in
// parseContextSummary for API and regression validation; compression itself
// must not discard a whole batch because a model omitted an empty field or
// returned an extra explanatory key.
func parseCompressionSummary(raw string) (contextSummary, error) {
	var out contextSummary
	if len(raw) > 256<<10 || !utf8.ValidString(raw) {
		return out, errors.New("summary_response_encoding_or_size_invalid")
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return out, fmt.Errorf("summary_schema: %w", err)
	}
	if out.Version == 0 {
		out.Version = 1
	}
	if out.People == nil {
		out.People = []summaryPerson{}
	}
	if out.Topics == nil {
		out.Topics = []summaryTopic{}
	}
	if out.Media == nil {
		out.Media = []summaryMedia{}
	}
	if out.PendingTasks == nil {
		out.PendingTasks = []summaryTask{}
	}
	if out.Preferences == nil {
		out.Preferences = []string{}
	}
	if out.Corrections == nil {
		out.Corrections = []string{}
	}
	if out.Version != 1 || len(out.People) > 20 || len(out.Topics) > 20 || len(out.Media) > 20 || len(out.PendingTasks) > 20 || len(out.Preferences) > 20 || len(out.Corrections) > 20 {
		return out, errors.New("summary_compression_shape_invalid")
	}
	canonical, _ := json.Marshal(out)
	if chars := utf8.RuneCount(canonical); len(canonical) > 14000 || chars > 3500 {
		return out, fmt.Errorf("summary_canonical_length_%d_exceeds_3500_characters", chars)
	}
	return out, nil
}

func budgetSummary(raw string, budget int) string {
	if raw == "" || raw == "null" {
		return "null"
	}
	s, err := parseContextSummary(raw, false)
	if err != nil {
		// Host-bound provenance can make a valid compressed summary exceed
		// the model response limit. It is stored for verification, not sent
		// to the conversation model. Decode the same strict schema and trim
		// it below instead of dropping the entire compressed history.
		if len(raw) <= 256<<10 && utf8.ValidString(raw) {
			var host contextSummary
			d := json.NewDecoder(bytes.NewBufferString(raw))
			d.DisallowUnknownFields()
			if d.Decode(&host) == nil && d.Decode(new(any)) == io.EOF && host.Version == 1 && len(host.Sources) > 0 && host.People != nil && host.Topics != nil && host.Media != nil && host.PendingTasks != nil && host.Preferences != nil && host.Corrections != nil && len(host.People) <= 20 && len(host.Topics) <= 20 && len(host.Media) <= 20 && len(host.PendingTasks) <= 20 && len(host.Preferences) <= 20 && len(host.Corrections) <= 20 {
				s = host
				err = nil
			}
		}
	}
	if err != nil {
		// Prior versions allowed different structures inside these same fields.
		// Keep that bounded legacy data until the next successful compression;
		// never silently erase the only memory of the already covered prefix.
		var old map[string]json.RawMessage
		if len(raw) > 14000 || json.Unmarshal([]byte(raw), &old) != nil || old == nil {
			return "null"
		}
		known := map[string]bool{"people": true, "topics": true, "current_focus": true, "media": true, "pending_tasks": true, "preferences": true, "corrections": true}
		for key := range old {
			if !known[key] {
				return "null"
			}
		}
		if len(raw) <= budget {
			return raw
		}
		limited := map[string]json.RawMessage{}
		for _, key := range []string{"current_focus", "pending_tasks", "media", "corrections", "preferences", "people", "topics"} {
			if value, ok := old[key]; ok {
				limited[key] = value
				b, _ := json.Marshal(limited)
				if len(b) > budget {
					delete(limited, key)
				}
			}
		}
		b, _ := json.Marshal(limited)
		return string(b)
	}
	s.Sources = nil
	for i := range s.People {
		s.People[i].SourceEventIDs = nil
	}
	for i := range s.Topics {
		s.Topics[i].SourceEventIDs = nil
	}
	for i := range s.Media {
		s.Media[i].SourceEventIDs = nil
	}
	for i := range s.PendingTasks {
		s.PendingTasks[i].SourceEventIDs = nil
	}
	if budget > 14000 {
		budget = 14000
	}
	// Drop the oldest lower-priority entries before recent media and pending work.
	for {
		b, _ := json.Marshal(s)
		if len(b) <= budget && utf8.RuneCount(b) <= 3500 {
			return string(b)
		}
		switch {
		case len(s.Topics) > 0:
			s.Topics = s.Topics[1:]
		case len(s.Media) > 1:
			s.Media = s.Media[1:]
		case len(s.Preferences) > 0:
			s.Preferences = s.Preferences[1:]
		case len(s.People) > 0:
			s.People = s.People[1:]
		case len(s.Corrections) > 1:
			s.Corrections = s.Corrections[1:]
		case len(s.PendingTasks) > 1:
			s.PendingTasks = s.PendingTasks[1:]
		default:
			return "null"
		}
	}
}
