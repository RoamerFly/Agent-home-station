// Package agentrun owns the lifecycle of one live conversation run. It knows
// nothing about WeChat, model providers, permission grants, or media codecs.
package agentrun

import (
	"context"
	"sync"
)

type Scope struct{ Account, Thread, Sender, RunID string }

type Event struct {
	ID     string `json:"message_id"`
	Thread string `json:"thread"`
	Sender string `json:"sender"`
	Text   string `json:"text"`
	At     string `json:"at,omitempty"`
	Kind   int    `json:"message_type,omitempty"`
}

// Prepare resolves observations outside the scheduler/queue lock. Commit marks
// the batch durably consumed BEFORE any tool may act on these observations.
type Prepare func(context.Context, Event) (Event, error)
type Commit func(context.Context, []Event) error

type Session struct {
	mu       sync.Mutex
	scope    Scope
	events   []Event
	seen     map[string]bool
	consumed map[string]bool
	closed   bool
	prepare  Prepare
	commit   Commit
}

func New(scope Scope, prepare Prepare, commit Commit) *Session {
	return &Session{scope: scope, seen: map[string]bool{}, consumed: map[string]bool{}, prepare: prepare, commit: commit}
}

// Offer never changes the run's verified principal. Other speakers receive
// independent turns even when they post to the same group.
func (s *Session) Offer(account string, e Event) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || e.ID == "" || e.ID == s.scope.RunID || s.scope.Sender == "" || account != s.scope.Account || e.Thread != s.scope.Thread || e.Sender != s.scope.Sender {
		return false
	}
	if !s.seen[e.ID] {
		s.events = append(s.events, e)
		s.seen[e.ID] = true
	}
	return true
}

func (s *Session) Pending() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events) > 0
}

func (s *Session) Drain(ctx context.Context) ([]Event, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	end := min(len(s.events), 8)
	events := append([]Event(nil), s.events[:end]...)
	s.events = s.events[end:]
	s.mu.Unlock()
	for i, e := range events {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if s.prepare != nil {
			var err error
			events[i], err = s.prepare(ctx, e)
			if err != nil {
				return nil, err
			}
		}
	}
	if len(events) > 0 && s.commit != nil {
		if err := s.commit(ctx, events); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range events {
		s.consumed[e.ID] = true
	}
	return events, nil
}

// Seal atomically closes the final-response race: an event either belongs to
// this run, or remains an independent durable inbox entry for the next run.
func (s *Session) Seal() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) > 0 {
		return false
	}
	s.closed = true
	return true
}

func (s *Session) Consumed(id string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.consumed[id]
}

type contextKey struct{}

func WithSession(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, contextKey{}, s)
}
func FromContext(ctx context.Context) *Session { s, _ := ctx.Value(contextKey{}).(*Session); return s }
