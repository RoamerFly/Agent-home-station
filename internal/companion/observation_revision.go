package companion

import (
	"errors"
	"strings"
	"time"
)

var ErrDeliveryMemoryConflict = errors.New("delivery memory content mismatch")

// AcceptedDelivery is for explicitly keyed manual replies. Polling a receipt
// after a restart can recover memory without appending the same reply twice.
func (s *Store) AcceptedDelivery(input Input, text, key string, c Config, now time.Time) error {
	if input.Preview || !c.Permissions.Remember {
		return nil
	}
	return s.scopeTransaction(ScopeID(input.Channel, input.Account, input.Thread), func(data *persisted) error {
		id := ScopeID(input.Channel, input.Account, input.Thread)
		scope, ok := data.Scopes[id]
		if !ok {
			return errors.New("character scope missing")
		}
		for _, turn := range scope.Messages {
			if turn.Role == "assistant" && turn.TraceID == key {
				if turn.Text != boundedText(text, 2048) {
					return ErrDeliveryMemoryConflict
				}
				return nil
			}
		}
		scope.Messages = append(scope.Messages, Turn{ReplyContext: cloneReplyContext(input.ReplyContext), TraceID: key, Role: "assistant", Text: boundedText(text, 2048), FullText: text, SenderName: c.Name, SenderNameSource: "character", Time: now})
		scope.LastInteraction = now
		recordAutonomyAccepted(&scope, input, c, now)
		clean, err := s.pruneStored(scope, c, now)
		if err != nil {
			return err
		}
		data.Scopes[id] = clean
		return nil
	})
}

// ReplaceObservation refreshes only a derived observation already attached to
// the same incoming message. A comparison prevents concurrent edits being lost.
func (s *Store) ReplaceObservation(scopeID, trace, before, after string) error {
	_, err := s.UpdateObservation(scopeID, trace, before, after)
	return err
}

// Report whether the optimistic replacement actually occurred. An archived or
// concurrently changed turn is never silently reported as updated or re-added.
func (s *Store) UpdateObservation(scopeID, trace, before, after string) (bool, error) {
	changed := false
	err := s.scopeTransaction(scopeID, func(data *persisted) error {
		scope, ok := data.Scopes[scopeID]
		if !ok {
			return nil
		}
		for i := range scope.Messages {
			turn := &scope.Messages[i]
			if turn.Role == "user" && turn.TraceID == trace && turn.Text == before {
				turn.Text = boundedInputMemory(after)
				if turn.Source != nil && turn.HasOriginal {
					// A refreshed model observation cannot rewrite the user's
					// original body or promote a vision summary to user speech.
					turn.FullObservation = after
					for j := range turn.IncomingMedia {
						turn.IncomingMedia[j].Observation = after
						turn.IncomingMedia[j].ObservationVersion++
						turn.IncomingMedia[j].ObservationState = "observed"
					}
				} else {
					turn.FullText = UserRequestText(after)
					turn.HasOriginal = true
					turn.FullObservation = strings.TrimPrefix(after, UserRequestText(after))
				}
				data.Scopes[scopeID] = scope
				changed = true
				break
			}
		}
		return nil
	})
	return changed && err == nil, err
}

// CacheIncomingObservation attaches a first successful on-demand read to its
// exact retained source. It does not recreate an archived or forgotten turn.
func (s *Store) CacheIncomingObservation(channel string, ref SourceRef, body string) error {
	// A parser can return whitespace when there was nothing usable to observe.
	// Do not turn that into a successful, terminal observation: callers may
	// still choose a more suitable reader later.  This check is deliberately
	// before the transaction so a malformed result cannot modify a forgotten
	// scope or recreate any derived state.
	if strings.TrimSpace(body) == "" || ref.EventID == "" || ref.Revision < 1 {
		return ErrMemoryConflict
	}
	id := ScopeID(channel, ref.Account, ref.Thread)
	return s.scopeTransaction(id, func(data *persisted) error {
		scope := data.Scopes[id]
		for i := range scope.Messages {
			t := &scope.Messages[i]
			if t.EventID != ref.EventID {
				continue
			}
			if t.Source == nil || *t.Source != ref || !t.HasOriginal || t.Role != "user" {
				return ErrMemoryConflict
			}
			for k := range t.IncomingMedia {
				m := &t.IncomingMedia[k]
				if m.Source.EventID != ref.EventID || m.Source.Revision != ref.Revision {
					continue
				}
				if m.ObservationState == "observed" && m.Observation != "" {
					return nil
				}
				m.Observation, m.ObservationState = body, "observed"
				m.ObservationVersion++
				t.FullObservation = body
				data.Scopes[id] = scope
				return nil
			}
		}
		return nil
	})
}
