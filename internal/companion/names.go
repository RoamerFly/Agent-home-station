package companion

import (
	"context"
	"sync"
	"wechat-bot/internal/identity"
)

type NameLookup interface {
	ResolveSpeaker(context.Context, string, string) (identity.Name, error)
}

// ResolveNames is bounded to the current conversation's participants. Results
// are metadata, never identity keys or instructions.
func ResolveNames(ctx context.Context, lookup NameLookup, thread string, senders []string) map[string]identity.Name {
	out := map[string]identity.Name{}
	if lookup == nil {
		return out
	}
	seen := map[string]bool{}
	jobs := make(chan string, len(senders))
	for _, sender := range senders {
		if sender != "" && !seen[sender] && len(seen) < 41 {
			seen[sender] = true
			jobs <- sender
		}
	}
	close(jobs)
	var mu sync.Mutex
	var wait sync.WaitGroup
	for i := 0; i < 4; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for sender := range jobs {
				if ctx.Err() != nil {
					return
				}
				name, err := lookup.ResolveSpeaker(ctx, thread, sender)
				if err == nil && identity.Valid(name) {
					mu.Lock()
					out[sender] = name
					mu.Unlock()
				}
			}
		}()
	}
	wait.Wait()
	return out
}

func ScopeSenders(scope Scope) []string {
	ids := []string{}
	for _, turn := range scope.Messages {
		if turn.Role == "user" && turn.Sender != "" {
			ids = append(ids, turn.Sender)
		}
	}
	return ids
}

func NamedScope(scope Scope, names map[string]identity.Name, character string) Scope {
	scope = copyScope(scope)
	for i := range scope.Messages {
		turn := &scope.Messages[i]
		if turn.Role == "assistant" {
			if turn.SenderName == "" {
				turn.SenderName, turn.SenderNameSource = identity.Clean(character), "character"
			}
			continue
		}
		if name, ok := names[turn.Sender]; ok && identity.Valid(name) {
			turn.SenderName, turn.SenderNameSource = name.Text, name.Source
		}
	}
	return scope
}

func (s *Store) UpdateNames(input Input, c Config, names map[string]identity.Name) error {
	if input.Preview || !c.Permissions.Remember {
		return nil
	}
	id := ScopeID(input.Channel, input.Account, input.Thread)
	s.mu.Lock()
	scope, ok := s.data.Scopes[id]
	needs := false
	if ok {
		for _, turn := range scope.Messages {
			if turn.Role == "assistant" && turn.SenderName == "" {
				needs = true
			}
			if name, exists := names[turn.Sender]; turn.Role == "user" && exists && identity.Valid(name) && (turn.SenderName != name.Text || turn.SenderNameSource != name.Source) {
				needs = true
			}
		}
	}
	s.mu.Unlock()
	if !needs {
		return nil
	}
	return s.scopeTransaction(ScopeID(input.Channel, input.Account, input.Thread), func(data *persisted) error {
		scope, ok := data.Scopes[id]
		if !ok {
			return nil
		}
		data.Scopes[id] = NamedScope(scope, names, c.Name)
		return nil
	})
}
