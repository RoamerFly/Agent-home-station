package companion

import (
	"context"
	"crypto/rand"
	"math/big"
	"time"
)

// Statistics describe model outcomes, not sending allowances. Accepted runs
// require an actual accepted-delivery commit; a reply decision is not delivery.
type AutonomyStats struct {
	Day               string `json:"day,omitempty"`
	Silent            int    `json:"silent"`
	ReplyPlans        int    `json:"reply_plans"`
	Failed            int    `json:"failed"`
	Accepted          int    `json:"accepted"`
	LastOutcomeTrace  string `json:"last_outcome_trace,omitempty"`
	LastAcceptedTrace string `json:"last_accepted_trace,omitempty"`
	LastReason        string `json:"last_reason,omitempty"`
}

func autonomyToday(stats AutonomyStats, c Config, now time.Time) AutonomyStats {
	if stats.Day != localDay(c, now) {
		return AutonomyStats{Day: localDay(c, now)}
	}
	return stats
}

func nextProactiveEvaluation(rule ProactiveRule, now time.Time) time.Time {
	base := time.Duration(rule.IdleMinutes) * time.Minute
	if base <= 0 {
		base = time.Hour
	}
	delay := base
	if n, err := rand.Int(rand.Reader, big.NewInt(int64(base))); err == nil {
		delay = base/2 + time.Duration(n.Int64())
	}
	return now.Add(delay)
}

func (s *Store) ProactiveOutcome(in Input, action, reason string, c Config, now time.Time) error {
	if in.Preview || in.Mode != "proactive" {
		return nil
	}
	return s.transaction(func(data *persisted) error {
		id := ScopeID(in.Channel, in.Account, in.Thread)
		scope, ok := data.Scopes[id]
		if !ok {
			return nil
		}
		stats := autonomyToday(scope.Autonomy, c, now)
		if in.TraceID != "" && stats.LastOutcomeTrace == in.TraceID {
			return nil
		}
		if reason == "invalid_model_decision" || reason == "invalid_tool_arguments" || reason == "tool_budget_exhausted" || reason == "tool_unavailable_or_budget_exhausted" {
			action = "failed"
		}
		switch action {
		case "reply":
			stats.ReplyPlans++
		case "silent", "wait":
			stats.Silent++
		default:
			stats.Failed++
		}
		stats.LastOutcomeTrace, stats.LastReason = in.TraceID, boundedText(reason, 512)
		scope.Autonomy = stats
		data.Scopes[id] = scope
		return nil
	})
}

func recordAutonomyAccepted(scope *Scope, in Input, c Config, now time.Time) {
	if in.Mode != "proactive" || in.TraceID == "" {
		return
	}
	stats := autonomyToday(scope.Autonomy, c, now)
	if stats.LastAcceptedTrace == in.TraceID {
		return
	}
	stats.Accepted++
	stats.LastAcceptedTrace = in.TraceID
	scope.Autonomy = stats
}

// Individual provider/tool timeouts remain. A task-wide timeout is optional;
// zero inherits cancellation from the worker instead of killing every long job.
func (c Config) TaskContext(parent context.Context) (context.Context, context.CancelFunc) {
	if seconds := c.ConversationConfig().TaskTimeoutSeconds; seconds > 0 {
		return context.WithTimeout(parent, time.Duration(seconds)*time.Second)
	}
	return context.WithCancel(parent)
}
