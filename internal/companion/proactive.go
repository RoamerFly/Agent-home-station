package companion

import (
	"time"
)

type ProactiveStatus struct {
	Reason string    `json:"reason"`
	Today  int       `json:"today"`
	NextAt time.Time `json:"next_at,omitempty"`
}

func proactiveToday(scope Scope, c Config, now time.Time) int {
	day := localDay(c, now)
	if scope.ProactiveDay == day {
		return scope.ProactiveCount
	}
	// Upgrade the original once-per-day ledger without resetting its claim.
	if scope.ProactiveDay == "" && !scope.LastProactive.IsZero() && localDay(c, scope.LastProactive) == day {
		return 1
	}
	return 0
}

func proactiveStatus(data persisted, channel, account, thread string, c Config, now time.Time) ProactiveStatus {
	scope := prune(data.Scopes[ScopeID(channel, account, thread)], c, now)
	state := ProactiveStatus{Today: proactiveToday(scope, c, now)}
	selected := false
	for _, id := range c.ProactiveThreads {
		if id == thread {
			selected = true
			break
		}
	}
	switch {
	case !selected:
		state.Reason = "not_selected"
	case !c.Enabled:
		state.Reason = "character_disabled"
	case !c.Permissions.Proactive:
		state.Reason = "proactive_permission_denied"
	case !c.Permissions.ReplyText:
		state.Reason = "text_reply_permission_denied"
	case c.Quiet(now):
		state.Reason = "quiet_hours"
	case data.ProactiveDay == localDay(c, now) && data.ProactiveCount >= c.ProactiveMaxPerDay:
		state.Reason = "proactive_daily_budget"
	case !c.Permissions.Remember || len(scope.Messages) == 0 && len(scope.Notes) == 0:
		state.Reason = "no_reliable_context"
	case scope.LastInteraction.IsZero() || now.Sub(scope.LastInteraction) > time.Duration(c.MemoryDays)*24*time.Hour:
		state.Reason = "context_expired"
	default:
		rule := c.ProactiveRule(thread)
		if state.Today >= rule.MaxPerDay {
			state.Reason = "target_daily_budget"
			break
		}
		last := scope.LastInteraction
		if scope.LastActivity.After(last) {
			last = scope.LastActivity
		}
		if scope.LastProactive.After(last) {
			last = scope.LastProactive
		}
		state.NextAt = last.Add(time.Duration(rule.IdleMinutes) * time.Minute)
		state.Reason = "ready"
		if now.Before(state.NextAt) {
			state.Reason = "idle_wait"
		}
	}
	return state
}

func (s *Store) proactiveStatusLocked(channel, account, thread string, c Config, now time.Time) ProactiveStatus {
	return proactiveStatus(s.data, channel, account, thread, c, now)
}

func (s *Store) ProactiveState(channel, account, thread string, c Config, now time.Time) ProactiveStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proactiveStatusLocked(channel, account, thread, c, now)
}

// TouchActivity records only scheduling metadata. Ordinary messages that do not
// trigger a reply must still postpone idle greetings, without being memorized.
func (s *Store) TouchActivity(input Input, now time.Time) error {
	return s.transaction(func(data *persisted) error {
		id := ScopeID(input.Channel, input.Account, input.Thread)
		scope, exists := data.Scopes[id]
		if !exists {
			// No reliable context means no possible idle greeting. Avoid
			// creating empty memory scopes for every untriggered conversation.
			return nil
		}
		if now.After(scope.LastActivity) {
			scope.LastActivity = now
		}
		data.Scopes[id] = scope
		return nil
	})
}
