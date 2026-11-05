package companion

import (
	"context"
	"crypto/rand"
	"math/big"
	"strings"
	"time"
)

// RecordStatus distinguishes a choice not to speak from a failed or skipped
// operation without changing the decision used by the sending machinery.
func (d Decision) RecordStatus() string {
	if d.Action != "silent" && d.Action != "wait" {
		return d.Action
	}
	if d.ReviewFailure != nil || strings.HasPrefix(d.Reason, "grounding_") || strings.Contains(d.Reason, "invalid") {
		return "failed"
	}
	switch d.Reason {
	case "tool_task_deadline_exceeded", "tool_budget_exhausted", "tool_unavailable_or_budget_exhausted", "unverified_model_response", "media_unavailable_or_permission_denied":
		return "failed"
	case "quiet_hours", "no_reliable_context":
		return "skipped"
	case "private_diagnostic_only", "cross_conversation_private_route_unavailable", "cross_conversation_media_requires_explicit_delivery":
		return "blocked"
	case "cross_conversation_result_private_delivery_handled":
		return "completed"
	}
	return d.Action
}

// Statistics describe model outcomes, not sending allowances. Accepted runs
// require an actual accepted-delivery commit; a reply decision is not delivery.
type AutonomyStats struct {
	Day               string `json:"day,omitempty"`
	Silent            int    `json:"silent"`
	ReplyPlans        int    `json:"reply_plans"`
	Failed            int    `json:"failed"`
	Skipped           int    `json:"skipped,omitempty"`
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
	return s.scopeTransaction(ScopeID(in.Channel, in.Account, in.Thread), func(data *persisted) error {
		id := ScopeID(in.Channel, in.Account, in.Thread)
		scope, ok := data.Scopes[id]
		if !ok {
			return nil
		}
		stats := autonomyToday(scope.Autonomy, c, now)
		if in.TraceID != "" && stats.LastOutcomeTrace == in.TraceID {
			return nil
		}
		switch action {
		case "reply":
			stats.ReplyPlans++
		case "silent", "wait":
			stats.Silent++
		case "skipped", "blocked":
			stats.Skipped++
		case "completed":
			// Private routing owns the separate delivery and its receipt.
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
