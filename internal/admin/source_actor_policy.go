package admin

import "context"

// sourceActorProof is attached only by an authenticated tool facade before it
// reads a different received message. The originating message author remains
// evidence provenance; this proof records the person currently asking to use
// that evidence and revalidates their authority before every provider or
// plugin step.
type sourceActorProof struct {
	tools                                      *permittedTools
	account, actor, traceSender, requestThread string
	trace, taskID                              string
	requestKey, sourceThread                   string
}

type sourceActorContextKey struct{}

// withSourceActor is deliberately unexported: callers must already hold the
// trusted permittedTools instance for the active Agent turn. The helper always
// leaves an explicit proof marker, even for invalid inputs: an explicit facade
// request must fail closed rather than silently becoming an automatic/native
// request owned by the source uploader.
func withSourceActor(ctx context.Context, p *permittedTools, requestKey string, sourceThread string) context.Context {
	proof := sourceActorProof{tools: p}
	if p == nil || p.manager == nil || requestKey == "" || sourceThread == "" || p.trace.Account == "" || p.trace.Thread == "" || p.trace.Preview || p.trace.ReportOnly {
		return context.WithValue(ctx, sourceActorContextKey{}, proof)
	}
	actor := p.serviceRequester()
	if actor == "" {
		return context.WithValue(ctx, sourceActorContextKey{}, proof)
	}
	proof = sourceActorProof{tools: p, account: p.trace.Account, actor: actor, traceSender: p.trace.Sender, requestThread: p.trace.Thread, trace: p.trace.TraceID, taskID: p.trace.TaskID, requestKey: requestKey, sourceThread: sourceThread}
	return context.WithValue(ctx, sourceActorContextKey{}, proof)
}

func sourceActorFromContext(ctx context.Context) (sourceActorProof, bool) {
	proof, ok := ctx.Value(sourceActorContextKey{}).(sourceActorProof)
	return proof, ok
}

func (p sourceActorProof) active() bool {
	if p.tools == nil || p.tools.manager == nil || p.actor == "" || p.account == "" || p.requestThread == "" || p.sourceThread == "" || p.requestKey == "" {
		return false
	}
	// A later SetTrace, account switch, or actor switch cannot reuse a proof
	// captured for the prior turn. conversationReadAllowed consults current
	// policy/owner settings and is intentionally evaluated again here.
	trace := p.tools.trace
	if trace.Account != p.account || trace.Sender != p.traceSender || trace.Thread != p.requestThread || trace.TraceID != p.trace || trace.TaskID != p.taskID || trace.Preview || trace.ReportOnly || p.tools.serviceRequester() != p.actor || !p.tools.manager.agent.config().Enabled || !p.tools.manager.agentTaskActive(trace) {
		return false
	}
	if trace.Surf && !p.tools.manager.surfAllowed(trace.Thread) {
		return false
	}
	if !p.tools.conversationReadAllowed(p.requestThread) || !p.tools.conversationReadAllowed(p.sourceThread) {
		return false
	}
	return true
}
