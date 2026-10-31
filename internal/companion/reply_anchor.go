package companion

import (
	"context"
	"errors"
)

// chooseReplyAnchor only follows an explicit, selected evidence chain. It never
// substitutes a participant's newest message merely because its author matches.
// strict is used for an existing binding: its exact anchor and version must
// survive, rather than falling back to another otherwise valid source.
func (e *Engine) chooseReplyAnchor(ctx context.Context, in Input, r ReplyContext, strict bool) (MemoryEvent, error) {
	if len(r.SourceEventIDs) == 0 && systemReplyContext(in) {
		validTargets := len(r.TargetIDs) == 0 && in.Sender == "" || len(r.TargetIDs) == 1 && r.TargetIDs[0] == in.Sender && in.Sender != ""
		if r.AnchorEventID != "" || r.AnchorMessageID != in.TraceID || r.QuoteRequired || !validTargets {
			return MemoryEvent{}, errors.New("reply_system_anchor_not_grounded")
		}
		return MemoryEvent{Trace: r.AnchorMessageID, Role: "system_context"}, nil
	}
	if len(r.SourceEventIDs) == 0 && ephemeralReplyContext(in, e.Config) {
		if r.AnchorEventID != "" || r.AnchorMessageID != in.Source.MessageID {
			return MemoryEvent{}, errors.New("reply_anchor_not_grounded")
		}
		return MemoryEvent{Trace: in.Source.MessageID, Sender: in.Sender, Role: "user"}, nil
	}
	if e.Store == nil || e.Store.db == nil {
		if in.Preview || ephemeralReplyContext(in, e.Config) || systemReplyContext(in) {
			return MemoryEvent{Trace: r.AnchorMessageID}, nil
		}
		return MemoryEvent{}, errors.New("reply_anchor_source_unavailable")
	}
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	selected := map[string]bool{}
	for _, id := range r.SourceEventIDs {
		selected[id] = true
	}
	current, hasCurrent := e.Store.SourceForInput(in)
	var trigger MemoryEvent
	if hasCurrent {
		trigger, _ = e.Store.ReadEvent(ctx, []string{scope}, current.EventID)
	}
	quoted, hasQuote := e.replyReferenceEvent(ctx, in, in.QuotedSource)
	allowedAnchor := func(ev MemoryEvent) bool {
		if ev.ID == "" || ev.Trace == "" || ev.Role != "user" {
			return false
		}
		if !selected[ev.ID] && ev.ID != trigger.ID && (!hasQuote || ev.ID != quoted.ID) {
			return false
		}
		for _, target := range r.TargetIDs {
			linked := ev.Sender == target
			for _, id := range ev.Addressees {
				linked = linked || id == target
			}
			if ev.ID == trigger.ID {
				for _, id := range in.Addressees {
					linked = linked || id == target
				}
			}
			if !linked {
				return false
			}
		}
		return true
	}
	read := func(id string) (MemoryEvent, error) {
		ev, err := e.Store.ReadEvent(ctx, []string{scope}, id)
		if err != nil {
			return MemoryEvent{}, err
		}
		if old, ok := r.SourceVersions[id]; ok && old != ev.Revision {
			return MemoryEvent{}, errors.New("reply_source_revision_changed")
		}
		return ev, nil
	}
	if r.AnchorEventID != "" {
		ev, err := read(r.AnchorEventID)
		if err != nil {
			return MemoryEvent{}, err
		}
		if r.AnchorMessageID != "" && r.AnchorMessageID != ev.Trace {
			return MemoryEvent{}, errors.New("reply_anchor_identity_mismatch")
		}
		if allowedAnchor(ev) {
			if strict && (!selected[ev.ID] || r.SourceVersions[ev.ID] != ev.Revision) {
				return MemoryEvent{}, errors.New("reply_anchor_version_missing")
			}
			return ev, nil
		}
		return MemoryEvent{}, errors.New("reply_anchor_not_grounded")
	}
	if r.AnchorMessageID != "" {
		ids := append([]string(nil), r.SourceEventIDs...)
		if trigger.ID != "" {
			ids = append(ids, trigger.ID)
		}
		if hasQuote {
			ids = append(ids, quoted.ID)
		}
		for _, id := range ids {
			ev, err := read(id)
			if err != nil {
				return MemoryEvent{}, err
			}
			if ev.Trace == r.AnchorMessageID && allowedAnchor(ev) {
				if strict && (!selected[ev.ID] || r.SourceVersions[ev.ID] != ev.Revision) {
					return MemoryEvent{}, errors.New("reply_anchor_version_missing")
				}
				return ev, nil
			}
		}
		return MemoryEvent{}, errors.New("reply_anchor_not_grounded")
	}
	if strict {
		return MemoryEvent{}, errors.New("reply_anchor_missing")
	}
	if hasQuote && allowedAnchor(quoted) {
		return quoted, nil
	}
	for _, id := range r.SourceEventIDs {
		ev, err := read(id)
		if err != nil {
			return MemoryEvent{}, err
		}
		if allowedAnchor(ev) {
			return ev, nil
		}
	}
	if allowedAnchor(trigger) {
		return trigger, nil
	}
	return MemoryEvent{}, errors.New("reply_retarget_quote_unavailable")
}

func (e *Engine) replyReferenceEvent(ctx context.Context, in Input, ref *SourceRef) (MemoryEvent, bool) {
	if ref == nil || e.Store == nil || e.Store.db == nil {
		return MemoryEvent{}, false
	}
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	matches := func(ev MemoryEvent) bool {
		if ref.Account != "" && ref.Account != in.Account || ref.Thread != "" && ref.Thread != in.Thread || ref.MessageID != "" && ref.MessageID != ev.Trace || ref.Sender != "" && ref.Sender != ev.Sender || ref.Revision > 0 && ref.Revision != ev.Revision {
			return false
		}
		if ref.InstanceID != "" && (ev.Source == nil || ref.InstanceID != ev.Source.InstanceID) {
			return false
		}
		if ref.ServerID != "" && (ev.Source == nil || ref.ServerID != ev.Source.ServerID) {
			return false
		}
		return ev.Role == "user"
	}
	if ref.EventID != "" {
		ev, err := e.Store.ReadEvent(ctx, []string{scope}, ref.EventID)
		return ev, err == nil && matches(ev)
	}
	if ref.MessageID == "" {
		return MemoryEvent{}, false
	}
	rows, err := e.Store.db.QueryContext(ctx, "SELECT id FROM events WHERE scope=? AND trace=? AND role='user'", scope, ref.MessageID)
	if err != nil {
		return MemoryEvent{}, false
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			_ = rows.Close()
			return MemoryEvent{}, false
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return MemoryEvent{}, false
	}
	var result MemoryEvent
	for _, id := range ids {
		if ev, err := e.Store.ReadEvent(ctx, []string{scope}, id); err == nil && matches(ev) {
			if result.ID != "" {
				return MemoryEvent{}, false
			}
			result = ev
		}
	}
	return result, result.ID != ""
}
