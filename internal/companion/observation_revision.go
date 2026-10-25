package companion

import (
	"errors"
	"time"
)

// AcceptedDelivery is for explicitly keyed manual replies. Polling a receipt
// after a restart can recover memory without appending the same reply twice.
func (s *Store) AcceptedDelivery(input Input, text, key string, c Config, now time.Time) error {
	if input.Preview || !c.Permissions.Remember {
		return nil
	}
	return s.transaction(func(data *persisted) error {
		id := ScopeID(input.Channel, input.Account, input.Thread)
		scope, ok := data.Scopes[id]
		if !ok {
			return errors.New("character scope missing")
		}
		for _, turn := range scope.Messages {
			if turn.Role == "assistant" && turn.TraceID == key {
				if turn.Text != boundedText(text, 2048) {
					return errors.New("delivery memory content mismatch")
				}
				return nil
			}
		}
		scope.Messages = append(scope.Messages, Turn{TraceID: key, Role: "assistant", Text: boundedText(text, 2048), SenderName: c.Name, SenderNameSource: "character", Time: now})
		scope.LastInteraction = now
		data.Scopes[id] = prune(scope, c, now)
		return nil
	})
}

// ReplaceObservation refreshes only a derived observation already attached to
// the same incoming message. A comparison prevents concurrent edits being lost.
func (s *Store) ReplaceObservation(scopeID, trace, before, after string) error {
	return s.transaction(func(data *persisted) error {
		scope, ok := data.Scopes[scopeID]
		if !ok {
			return nil
		}
		for i := range scope.Messages {
			turn := &scope.Messages[i]
			if turn.Role == "user" && turn.TraceID == trace && turn.Text == before {
				turn.Text = boundedText(after, 2048)
				data.Scopes[scopeID] = scope
				break
			}
		}
		return nil
	})
}
