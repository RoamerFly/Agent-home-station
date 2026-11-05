package companion

import (
	"context"
	"encoding/json"
	"wechat-bot/internal/agentrun"
)

// AgentRunner is the single model -> tool -> feedback -> model loop. Transport
// adapters supply observations and durable event acknowledgements; they do not
// run a second model to decide whether to discard the in-flight task.
type AgentRunner struct {
	*Engine
	session *agentrun.Session
}

func (e *Engine) decide(ctx context.Context, system string, turns []map[string]string) (Decision, error) {
	r := &AgentRunner{Engine: e, session: agentrun.FromContext(ctx)}
	return r.Run(ctx, system, turns)
}

func (r *AgentRunner) followups(ctx context.Context, turns *[]map[string]string) (bool, error) {
	events, err := r.session.Drain(ctx)
	if err != nil || len(events) == 0 {
		return false, err
	}
	// These events have already passed the session's identity and durable
	// consumption checks. Keep tool intent validation in step with the model.
	if observer, ok := r.Tools.(interface{ ObserveFollowups([]agentrun.Event) }); ok {
		observer.ObserveFollowups(events)
	}
	b, _ := json.Marshal(map[string]any{"kind": "current_followup", "events": r.followupEvidence(*turns, events)})
	*turns = append(*turns, map[string]string{"role": "user", "content": string(b)})
	return true, nil
}
