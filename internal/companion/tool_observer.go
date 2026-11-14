package companion

import (
	"context"
	"errors"
	"time"

	"wechat-bot/internal/identity"
	"wechat-bot/internal/mcptools"
)

// ToolDiagnostic contains execution metadata only. Arguments, result bodies,
// arbitrary provider errors and model reasoning must never enter this stream.
type ToolDiagnostic struct {
	Event, CallID, Tool, Status, Code, Stage string
	Attempt, Retries, Requests, HTTPStatus   int
	DurationMS                               int64
}

type toolObserverKey struct{}
type toolInvocationKey struct{}
type toolInvocation struct {
	id, name string
	attempt  int
}

func WithToolObserver(ctx context.Context, observe func(ToolDiagnostic)) context.Context {
	return context.WithValue(ctx, toolObserverKey{}, observe)
}

func withToolInvocation(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, toolInvocationKey{}, &toolInvocation{id: identity.RandomToken(), name: name})
}

func emitToolDiagnostic(ctx context.Context, diagnostic ToolDiagnostic) {
	if invocation, ok := ctx.Value(toolInvocationKey{}).(*toolInvocation); ok {
		diagnostic.CallID, diagnostic.Tool = invocation.id, invocation.name
		diagnostic.Attempt, diagnostic.Retries = invocation.attempt, max(invocation.attempt-1, 0)
	}
	if observe, ok := ctx.Value(toolObserverKey{}).(func(ToolDiagnostic)); ok && observe != nil {
		observe(diagnostic)
	}
}

func observedToolCall(ctx context.Context, call func(context.Context) (string, error)) (string, error) {
	if invocation, ok := ctx.Value(toolInvocationKey{}).(*toolInvocation); ok {
		invocation.attempt++
	}
	started := time.Now()
	emitToolDiagnostic(ctx, ToolDiagnostic{Event: "tool_started", Status: "running", Stage: "invocation"})
	result, err := call(ctx)
	diagnostic := ToolDiagnostic{Event: "tool_completed", Status: "succeeded", Stage: "invocation", DurationMS: time.Since(started).Milliseconds()}
	if err != nil {
		diagnostic.Event, diagnostic.Status, diagnostic.Code = "tool_failed", "failed", "tool_failed"
		var safe interface{ SafeToolFailure() (string, string) }
		var web *mcptools.WebError
		switch {
		case errors.Is(err, context.Canceled):
			diagnostic.Event, diagnostic.Status, diagnostic.Code = "tool_cancelled", "cancelled", "request_cancelled"
		case errors.Is(err, context.DeadlineExceeded):
			diagnostic.Code = "tool_timeout"
		case errors.As(err, &web):
			diagnostic.Code, diagnostic.Stage = web.Code, web.Stage
			diagnostic.Requests, diagnostic.HTTPStatus = web.Attempts, web.Status
		case errors.As(err, &safe):
			diagnostic.Code, _ = safe.SafeToolFailure()
		}
	}
	emitToolDiagnostic(ctx, diagnostic)
	return result, err
}
