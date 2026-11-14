package companion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"wechat-bot/internal/mcptools"
)

type observedRetryTools struct{ attempts int }

func (*observedRetryTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "web_search", InputSchema: json.RawMessage(`{}`)}}, nil
}
func (tools *observedRetryTools) Call(context.Context, string, json.RawMessage) (string, error) {
	tools.attempts++
	if tools.attempts == 1 {
		return "private response body", &mcptools.WebError{Code: "network_timeout", Stage: "read", Attempts: 1}
	}
	return "source https://example.org/ correct result", nil
}

func TestToolObserverRecordsEveryRealAttemptAndRetry(t *testing.T) {
	model := &toolModel{name: "web_search"}
	engine, err := NewPreview(enabledConfig(), model, "global")
	if err != nil {
		t.Fatal(err)
	}
	tools := &observedRetryTools{}
	engine.Tools, engine.MaxToolCalls = tools, 2
	events := []ToolDiagnostic{}
	ctx := WithToolObserver(context.Background(), func(d ToolDiagnostic) { events = append(events, d) })
	decision, err := engine.Respond(ctx, Input{Mode: "direct", Text: "查一下", Preview: true})
	if err != nil || decision.Action != "reply" || tools.attempts != 2 || len(events) != 5 {
		t.Fatal(decision, err, tools.attempts, events)
	}
	for index, event := range []string{"tool_started", "tool_failed", "tool_retry_scheduled", "tool_started", "tool_completed"} {
		if events[index].Event != event || events[index].Tool != "web_search" || events[index].CallID == "" || events[index].CallID != events[0].CallID {
			t.Fatal("tool lifecycle is incomplete", events)
		}
	}
	if events[1].Code != "network_timeout" || events[1].Stage != "read" || events[3].Attempt != 2 || events[4].Retries != 1 {
		t.Fatal("retry identity missing", events)
	}
	raw, _ := json.Marshal(events)
	if strings.Contains(string(raw), "private response") || strings.Contains(string(raw), "public topic") {
		t.Fatal("tool contents leaked", string(raw))
	}
}

func TestToolObserverSeparatesCallsAndReportsCancellationWithoutRawError(t *testing.T) {
	events := []ToolDiagnostic{}
	ctx := WithToolObserver(context.Background(), func(d ToolDiagnostic) { events = append(events, d) })
	for _, err := range []error{errors.New("private API token response"), context.Canceled} {
		callCtx := withToolInvocation(ctx, "synthetic_tool")
		_, _ = observedToolCall(callCtx, func(context.Context) (string, error) { return "private body", err })
	}
	if len(events) != 4 || events[1].Code != "tool_failed" || events[3].Event != "tool_cancelled" || events[0].CallID == events[2].CallID {
		t.Fatal(events)
	}
	raw, _ := json.Marshal(events)
	if strings.Contains(string(raw), "private") {
		t.Fatal("provider contents leaked", string(raw))
	}
}
