package companion

import (
	"errors"
	"slices"
	"strings"
	"time"
)

var (
	ErrDeliveryMemoryConflict   = errors.New("delivery memory content mismatch")
	ErrDeliveryIdentityRequired = errors.New("delivery memory identity required")
)

// AcceptedDelivery records an accepted outgoing bubble identified by a durable
// transport or host operation identity. Polling the same receipt after a
// restart recovers memory without appending the same bubble twice. Callers
// must not derive key from text or an incoming request trace: identical text
// can be sent as two distinct bubbles, while one incoming request can produce
// several bubbles.
func (s *Store) AcceptedDelivery(input Input, text, key string, c Config, now time.Time) error {
	return s.acceptedDelivery(input, text, key, "", c, now)
}

// AcceptedDeliveryWithLegacyReceipt upgrades only the deterministic receipt
// identity written by the pre-upgrade delivery recovery path. legacyKey must
// be derived from a currently confirmed native idempotency key, native job ID
// and immutable creation timestamp; plan-slot keys and text hashes are not
// accepted aliases.
func (s *Store) AcceptedDeliveryWithLegacyReceipt(input Input, text, key, legacyKey string, c Config, now time.Time) error {
	return s.acceptedDelivery(input, text, key, legacyKey, c, now)
}

func (s *Store) acceptedDelivery(input Input, text, key, legacyKey string, c Config, now time.Time) error {
	if input.Preview || !c.Permissions.Remember {
		return nil
	}
	key = strings.TrimSpace(key)
	legacyKey = strings.TrimSpace(legacyKey)
	if key == "" {
		return ErrDeliveryIdentityRequired
	}
	return s.scopeTransaction(ScopeID(input.Channel, input.Account, input.Thread), func(data *persisted) error {
		id := ScopeID(input.Channel, input.Account, input.Thread)
		scope, ok := data.Scopes[id]
		if !ok {
			return errors.New("character scope missing")
		}
		for i := range scope.Messages {
			turn := &scope.Messages[i]
			// TraceID fallback is only for records written before DeliveryID was
			// introduced. It lets recovery finish safely without fabricating an
			// identity for historical bubbles.
			if turn.Role == "assistant" && (turn.DeliveryID == key || turn.DeliveryID == "" && turn.TraceID == key) {
				if turn.Text != boundedText(text, 2048) {
					return ErrDeliveryMemoryConflict
				}
				return nil
			}
			if legacyKey != "" && turn.Role == "assistant" && turn.DeliveryID == "" && turn.TraceID == legacyKey {
				if turn.Text != boundedText(text, 2048) {
					return ErrDeliveryMemoryConflict
				}
				// This is the only migration alias. It is backed by the same
				// native receipt currently being reconciled, so filling the new
				// identity does not invent one for ambiguous old records.
				turn.DeliveryID = key
				turn.RequestTraceID = input.TraceID
				data.Scopes[id] = scope
				return nil
			}
		}
		scope.Messages = append(scope.Messages, Turn{ReplyContext: cloneReplyContext(input.ReplyContext), DeliveryID: key, RequestTraceID: input.TraceID, TraceID: key, Role: "assistant", Text: boundedText(text, 2048), FullText: text, SenderName: c.Name, SenderNameSource: "character", Time: now})
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

// UpdateObservationWithCoverage is the trusted-observer counterpart to
// UpdateObservation.  Coverage is supplied by the host that actually decoded
// the media; Store never derives it from arbitrary user-visible text.
func (s *Store) UpdateObservationWithCoverage(scopeID, trace, before, after, observedRange, observedExcerpt string, parts ...ObservationPart) (bool, error) {
	return s.updateObservation(scopeID, trace, before, after, observedRange, observedExcerpt, parts)
}

// Report whether the optimistic replacement actually occurred. An archived or
// concurrently changed turn is never silently reported as updated or re-added.
func (s *Store) UpdateObservation(scopeID, trace, before, after string) (bool, error) {
	return s.updateObservation(scopeID, trace, before, after, "", "", nil)
}

func (s *Store) updateObservation(scopeID, trace, before, after, observedRange, observedExcerpt string, parts []ObservationPart) (bool, error) {
	changed := false
	err := s.scopeTransaction(scopeID, func(data *persisted) error {
		scope, ok := data.Scopes[scopeID]
		if !ok {
			return nil
		}
		for i := range scope.Messages {
			turn := &scope.Messages[i]
			if turn.Role == "user" && turn.TraceID == trace && turn.Text == before {
				// A stale metadata refresh must not replace a just-completed
				// firsthand observation between the caller's read and this write.
				if len(parts) > 0 && !HasFirsthandObservation(parts) && len(turn.IncomingMedia) == 1 && HasFirsthandObservation(turn.IncomingMedia[0].ObservationParts) {
					return nil
				}
				turn.Text = boundedInputMemory(after)
				if turn.Source != nil && turn.HasOriginal {
					// A refreshed model observation cannot rewrite the user's
					// original body or promote a vision summary to user speech.
					turn.FullObservation = after
					for j := range turn.IncomingMedia {
						turn.IncomingMedia[j].Observation = after
						turn.IncomingMedia[j].ObservationVersion++
						turn.IncomingMedia[j].ObservationState = "observed"
						// A generic refresh must not preserve stale coverage.  A
						// one-message refresh has no media selector, so never copy
						// one observer's evidence across a multi-attachment event.
						turn.IncomingMedia[j].ObservedRange = ""
						turn.IncomingMedia[j].ObservationExcerpt = ""
						turn.IncomingMedia[j].ObservationParts = nil
						if len(turn.IncomingMedia) == 1 && (observedRange != "" || len(parts) > 0) {
							turn.IncomingMedia[j].ObservedRange = observedRange
							turn.IncomingMedia[j].ObservationExcerpt = observedExcerpt
							turn.IncomingMedia[j].ObservationParts = slices.Clone(parts)
						}
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
	return s.cacheIncomingObservation(channel, ref, body, "", "", nil)
}

// CacheIncomingObservationWithCoverage attaches a host-verified media range
// alongside its observation.  It intentionally takes the range explicitly so
// cached user/card text can never manufacture read coverage.
func (s *Store) CacheIncomingObservationWithCoverage(channel string, ref SourceRef, body, observedRange, observedExcerpt string, parts ...ObservationPart) error {
	return s.cacheIncomingObservation(channel, ref, body, observedRange, observedExcerpt, parts)
}

func (s *Store) cacheIncomingObservation(channel string, ref SourceRef, body, observedRange, observedExcerpt string, parts []ObservationPart) error {
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
				existingFirsthand := HasFirsthandObservation(m.ObservationParts) || (len(m.ObservationParts) == 0 && m.ObservedRange != "" && m.ObservationExcerpt != "")
				incomingFirsthand := HasFirsthandObservation(parts) || (len(parts) == 0 && observedRange != "" && observedExcerpt != "")
				// A late card/publisher cache must never erase a completed actual
				// read. Repeating the same observer result is a no-op as well.
				plainCache := observedRange == "" && len(parts) == 0
				sameResult := m.Observation == body && m.ObservedRange == observedRange && m.ObservationExcerpt == observedExcerpt && slices.Equal(m.ObservationParts, parts)
				if m.ObservationState == "observed" && m.Observation != "" && (plainCache || (existingFirsthand && !incomingFirsthand) || sameResult) {
					return nil
				}
				m.Observation, m.ObservationState = body, "observed"
				m.ObservedRange = observedRange
				m.ObservationExcerpt = observedExcerpt
				m.ObservationParts = slices.Clone(parts)
				m.ObservationVersion++
				t.FullObservation = body
				data.Scopes[id] = scope
				return nil
			}
		}
		return nil
	})
}
