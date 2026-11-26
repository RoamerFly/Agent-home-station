package companion

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// freshRoundInput drops only derived, per-attempt material. The original
// message, source identity and the task association stay available so the
// host can process the request again without disclosing a retry marker to the
// model. The delivery ledger and durable media/source records are untouched.
func freshRoundInput(in Input) Input {
	in.RecoveryToolResults = nil
	in.RecoveryEvidence = nil
	in.RecoveryPrivacy = nil
	in.RecoveryBlocked = false
	in.RequestScopedReply = false
	in.PlannedReviews = nil
	in.Text = UserRequestText(in.Text)
	in.IncomingMedia = freshMediaMetadata(in.IncomingMedia)
	return in
}

// The host keeps the fresh task identity for scheduling and tool ownership.
// The model receives the original inbound source as the current message, not
// a task-recovery envelope or an explanation that this is another attempt.
func freshRoundModelInput(in Input) Input {
	in.TaskID = ""
	in.BackgroundResult = false
	in.OriginTraceID = ""
	in.ReportOnly = false
	in.HistoricalReview = false
	in.RequestedText = ""
	in.ConversationDelivery = false
	in.ReplayNonce = ""
	in.PlannedReviews = nil
	if in.Source != nil && in.Source.MessageID != "" {
		in.TraceID = in.Source.MessageID
	}
	return in
}

func freshMediaMetadata(media []IncomingMediaRef) []IncomingMediaRef {
	out := cloneIncomingMedia(media)
	for i := range out {
		out[i].Observation = ""
		out[i].ObservationExcerpt = ""
		out[i].ObservationParts = nil
		out[i].ObservationState = ""
		out[i].ObservationVersion = 0
		out[i].ObservedRange = ""
	}
	return out
}

// A task continuation can use a new execution trace while still referring to
// the original native source. Both identities are checked because old accepted
// messages may have been recorded under either one.
func freshRoundTraces(in Input) map[string]bool {
	traces := map[string]bool{}
	for _, id := range []string{in.OriginTraceID, in.FreshRoundPriorTraceID} {
		if id != "" {
			traces[id] = true
		}
	}
	if in.Source != nil && in.Source.MessageID != "" {
		traces[in.Source.MessageID] = true
	}
	for _, unread := range in.UnreadMessages {
		if unread.MessageID != "" {
			traces[unread.MessageID] = true
		}
	}
	for _, id := range in.FreshRoundSourceMessageIDs {
		if id != "" {
			traces[id] = true
		}
	}
	return traces
}

func freshRoundIsolation(in Input) bool { return in.FreshRoundRetry || in.FreshRoundLineage }

func freshRoundUnbound(in Input) bool {
	return freshRoundIsolation(in) && (in.Source == nil || in.Source.EventID == "")
}

func freshRoundEventIDs(in Input) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(source *SourceRef) {
		if source != nil && source.EventID != "" && !seen[source.EventID] {
			seen[source.EventID] = true
			out = append(out, source.EventID)
		}
	}
	add(in.Source)
	for _, unread := range in.UnreadMessages {
		add(unread.Source)
	}
	for _, id := range in.FreshRoundExcludedEventIDs {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// FreshRoundEventIDs locates accepted assistant events associated with the
// prior attempt, including earlier continuations of the same task. Their IDs
// are used only to mask this attempt's read view; no row is deleted.
func (s *Store) FreshRoundEventIDs(ctx context.Context, in Input) ([]string, error) {
	if s == nil || !freshRoundIsolation(in) {
		return nil, nil
	}
	traces := freshRoundTraces(in)
	exact := make([]string, 0, len(traces))
	for trace := range traces {
		exact = append(exact, trace)
	}
	sort.Strings(exact)
	predicates := make([]string, 0, len(exact)+1)
	args := []any{ScopeID(in.Channel, in.Account, in.Thread)}
	for _, trace := range exact {
		predicates = append(predicates, "(e.trace=? OR COALESCE(json_extract(e.payload,'$.request_trace_id'),'')=?)")
		args = append(args, trace, trace)
	}
	if prefix := freshRoundTaskTracePrefix(in); prefix != "" {
		predicates = append(predicates, "(substr(e.trace,1,?)=? OR substr(COALESCE(json_extract(e.payload,'$.request_trace_id'),''),1,?)=?)")
		args = append(args, len(prefix), prefix, len(prefix), prefix)
	}
	if len(predicates) == 0 {
		return freshRoundEventIDs(in), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, errors.New("memory database closed")
	}
	currentPrefix := ""
	if in.TaskID != "" {
		currentPrefix = "task-" + in.TaskID + "-"
	}
	query := "SELECT e.id FROM events e WHERE e.scope=? AND e.role='assistant' AND (" + strings.Join(predicates, " OR ") + ")"
	if currentPrefix != "" {
		query += " AND NOT (substr(e.trace,1,?)=? OR substr(COALESCE(json_extract(e.payload,'$.request_trace_id'),''),1,?)=?)"
		args = append(args, len(currentPrefix), currentPrefix, len(currentPrefix), currentPrefix)
	}
	query += " ORDER BY e.seq"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := freshRoundEventIDs(in)
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id != "" && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids, rows.Err()
}

func freshRoundTaskTracePrefix(in Input) string {
	if in.FreshRoundPriorTaskID == "" {
		return ""
	}
	return "task-" + in.FreshRoundPriorTaskID + "-"
}

func freshRoundOwnsTrace(in Input, trace string, exact map[string]bool) bool {
	if trace == "" {
		return false
	}
	return exact[trace] || freshRoundTaskTracePrefix(in) != "" && strings.HasPrefix(trace, freshRoundTaskTracePrefix(in))
}

func freshRoundScope(scope Scope, in Input) Scope {
	if !freshRoundIsolation(in) {
		return scope
	}
	if freshRoundUnbound(in) {
		// The native original may be available even when older memory lacks
		// an event ID. Avoid guessing which historical fact belongs to it.
		// Only this model view is empty; current_message is assembled from
		// the host's verified native request outside the Scope.
		scope.Messages = nil
		scope.Summary = ""
		scope.Notes = nil
		scope.Preferences = ""
		return scope
	}
	traces := freshRoundTraces(in)
	eventIDs := map[string]bool{}
	for _, id := range freshRoundEventIDs(in) {
		eventIDs[id] = true
	}
	for i, turn := range scope.Messages {
		if turn.Role == "user" && traces[turn.TraceID] {
			eventIDs[eventID(scope.ID, turn, i)] = true
		}
		if turn.Role == "assistant" && (freshRoundOwnsTrace(in, turn.RequestTraceID, traces) || turn.Media != nil && freshRoundOwnsTrace(in, turn.Media.RequestTraceID, traces)) {
			eventIDs[eventID(scope.ID, turn, i)] = true
		}
	}
	kept := make([]Turn, 0, len(scope.Messages))
	for _, turn := range scope.Messages {
		if turn.Role == "assistant" && (freshRoundOwnsTrace(in, turn.RequestTraceID, traces) || turn.Media != nil && freshRoundOwnsTrace(in, turn.Media.RequestTraceID, traces)) {
			continue
		}
		if turn.Role == "user" && traces[turn.TraceID] {
			turn.IncomingMedia = freshMediaMetadata(turn.IncomingMedia)
			turn.FullObservation = ""
			turn.Text = UserRequestText(turn.Text)
			// The source remains in the store, but the context serializer must
			// not rehydrate its old observation from SQLite for this attempt.
			turn.EventID = ""
		}
		kept = append(kept, turn)
	}
	scope.Messages = kept
	scope.Summary = freshRoundSummary(scope.Summary, eventIDs)
	// Notes have no source identity. Do not expose an old draft through a
	// free-text note during this one request.
	scope.Notes = nil
	return scope
}

func freshRoundSummary(raw string, eventIDs map[string]bool) string {
	if raw == "" || raw == "null" {
		return raw
	}
	var summary contextSummary
	if json.Unmarshal([]byte(raw), &summary) != nil || summary.Version != 1 {
		return ""
	}
	linked := func(ids []string) bool {
		for _, id := range ids {
			if eventIDs[id] {
				return true
			}
		}
		return false
	}
	people := summary.People[:0]
	for _, item := range summary.People {
		if !linked(item.SourceEventIDs) {
			people = append(people, item)
		}
	}
	summary.People = people
	topics := summary.Topics[:0]
	for _, item := range summary.Topics {
		if !linked(item.SourceEventIDs) {
			topics = append(topics, item)
		}
	}
	summary.Topics = topics
	media := summary.Media[:0]
	for _, item := range summary.Media {
		if !linked(item.SourceEventIDs) {
			media = append(media, item)
		}
	}
	summary.Media = media
	tasks := summary.PendingTasks[:0]
	for _, item := range summary.PendingTasks {
		if !linked(item.SourceEventIDs) {
			tasks = append(tasks, item)
		}
	}
	summary.PendingTasks = tasks
	sources := summary.Sources[:0]
	for _, source := range summary.Sources {
		if !eventIDs[source.EventID] {
			sources = append(sources, source)
		}
	}
	summary.Sources = sources
	// These strings have no per-claim source link. They remain durable, but
	// cannot safely enter the isolated model view of this one attempt.
	summary.CurrentFocus = ""
	summary.Preferences = []string{}
	summary.Corrections = []string{}
	b, err := json.Marshal(summary)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
