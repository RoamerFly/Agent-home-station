package companion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
)

var ErrSummarySourceMissing = errors.New("summary_item_source_missing")
var ErrSummarySourceInvalid = errors.New("summary_item_source_invalid")

func (e *Engine) bindSummarySources(ctx context.Context, in Input, s *contextSummary, selected []Turn, previous []SourceRef) error {
	refs := map[string]SourceRef{}
	for _, t := range selected {
		if t.EventID != "" && t.Role == "user" {
			r := SourceRef{EventID: t.EventID, Sender: t.Sender, MessageID: t.TraceID}
			if t.Source != nil {
				r = *t.Source
				r.EventID = t.EventID
			}
			ev, err := e.Store.ReadEvent(ctx, []string{ScopeID(in.Channel, in.Account, in.Thread)}, t.EventID)
			if err != nil {
				return err
			}
			r.Revision = ev.Revision
			refs[t.EventID] = r
		}
	}
	// Previous provenance comes from the saved host snapshot, not a model's
	// new sources array. An invented source cannot introduce an unseen event.
	for _, r := range previous {
		ev, err := e.Store.ReadEvent(ctx, []string{ScopeID(in.Channel, in.Account, in.Thread)}, r.EventID)
		if err == nil && ev.Revision == r.Revision {
			refs[r.EventID] = r
		}
	}
	used := map[string]bool{}
	validate := func(ids []string) error {
		if len(ids) == 0 && in.Source != nil {
			return ErrSummarySourceMissing
		}
		for _, id := range ids {
			if _, ok := refs[id]; !ok {
				return ErrSummarySourceInvalid
			}
			used[id] = true
		}
		return nil
	}
	for _, x := range s.People {
		if err := validate(x.SourceEventIDs); err != nil {
			return err
		}
	}
	for _, x := range s.Topics {
		if err := validate(x.SourceEventIDs); err != nil {
			return err
		}
	}
	for _, x := range s.Media {
		if err := validate(x.SourceEventIDs); err != nil {
			return err
		}
	}
	for _, x := range s.PendingTasks {
		if err := validate(x.SourceEventIDs); err != nil {
			return err
		}
	}
	s.Sources = nil
	ids := make([]string, 0, len(used))
	for id := range used {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s.Sources = append(s.Sources, refs[id])
	}
	return nil
}
func (e *Engine) groundedSummary(in Input, raw string) string {
	if raw == "null" {
		return raw
	}
	var s contextSummary
	if json.Unmarshal([]byte(raw), &s) != nil || len(s.Sources) == 0 {
		return "null"
	}
	allowed := map[string]bool{}
	for _, r := range s.Sources {
		if !relevantSender(in, r.Sender) {
			continue
		}
		ev, err := e.Store.ReadEvent(context.Background(), []string{ScopeID(in.Channel, in.Account, in.Thread)}, r.EventID)
		if err == nil && ev.Revision == r.Revision {
			allowed[r.EventID] = true
		}
	}
	supported := func(ids []string) bool {
		if len(ids) == 0 {
			return false
		}
		for _, id := range ids {
			if !allowed[id] {
				return false
			}
		}
		return true
	}
	people := s.People[:0]
	for _, x := range s.People {
		if supported(x.SourceEventIDs) {
			people = append(people, x)
		}
	}
	s.People = people
	topics := s.Topics[:0]
	for _, x := range s.Topics {
		if supported(x.SourceEventIDs) {
			topics = append(topics, x)
		}
	}
	s.Topics = topics
	media := s.Media[:0]
	for _, x := range s.Media {
		if supported(x.SourceEventIDs) {
			media = append(media, x)
		}
	}
	s.Media = media
	tasks := s.PendingTasks[:0]
	for _, x := range s.PendingTasks {
		if supported(x.SourceEventIDs) {
			tasks = append(tasks, x)
		}
	}
	s.PendingTasks = tasks
	// Legacy free-text fields have no per-claim identity evidence.
	s.CurrentFocus = ""
	s.Preferences = []string{}
	s.Corrections = []string{}
	b, _ := json.Marshal(s)
	return string(b)
}

func summaryFingerprint(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
func (e *Engine) validSummaryPacketSources(ctx context.Context, scope string, raw json.RawMessage) bool {
	var summary contextSummary
	if json.Unmarshal(raw, &summary) != nil {
		return false
	}
	for _, ref := range summary.Sources {
		ev, err := e.Store.ReadEvent(ctx, []string{scope}, ref.EventID)
		if err != nil || ev.Revision != ref.Revision {
			return false
		}
	}
	return true
}
