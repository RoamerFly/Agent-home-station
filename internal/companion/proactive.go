package companion

import (
	"time"
)

type ProactiveStatus struct {
	Reason   string        `json:"reason"`
	Today    int           `json:"today"`
	NextAt   time.Time     `json:"next_at,omitempty"`
	Outcomes AutonomyStats `json:"outcomes"`
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
	state.Outcomes = autonomyToday(scope.Autonomy, c, now)
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
	case !c.Permissions.Remember || len(scope.Messages) == 0 && len(scope.Notes) == 0:
		state.Reason = "no_reliable_context"
	case scope.LastInteraction.IsZero() || now.Sub(scope.LastInteraction) > time.Duration(c.MemoryDays)*24*time.Hour:
		state.Reason = "context_expired"
	default:
		state.NextAt = scope.NextProactiveAt
		if state.NextAt.IsZero() {
			// Upgrade legacy schedules without letting ongoing group chatter
			// postpone the first evaluation forever. Claims then persist jitter.
			anchor := scope.LastProactive
			if anchor.IsZero() && len(scope.Messages) > 0 {
				anchor = scope.Messages[0].Time
			}
			if anchor.IsZero() {
				anchor = scope.LastInteraction
			}
			state.NextAt = anchor.Add(time.Duration(c.ProactiveRule(thread).IdleMinutes) * time.Minute)
		}
		state.Reason = "ready"
		if now.Before(state.NextAt) {
			state.Reason = "scheduled_wait"
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
// trigger a reply update activity without postponing autonomous evaluations.
func (s *Store) TouchActivity(input Input, now time.Time) error {
	return s.transaction(func(data *persisted) error {
		id := ScopeID(input.Channel, input.Account, input.Thread)
		scope, exists := data.Scopes[id]
		if !exists {
			// No reliable context means no possible autonomous review. Avoid
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
