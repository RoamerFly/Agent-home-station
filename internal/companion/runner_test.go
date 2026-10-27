package companion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/agentrun"
)

type runnerModelFunc func(context.Context, string, []map[string]string) (string, error)

func (f runnerModelFunc) Chat(ctx context.Context, s string, t []map[string]string) (string, error) {
	return f(ctx, s, t)
}

func TestAgentRunnerReplansStaleAnswerWithoutReplayingCompletedTool(t *testing.T) {
	s := agentrun.New(agentrun.Scope{Account: "a", Thread: "t", Sender: "s", RunID: "r"}, nil, nil)
	tools := &toolFixture{}
	steps := 0
	m := runnerModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
		steps++
		switch steps {
		case 1:
			return `{"action":"tool","tool":"web_search","arguments":{"query":"hello"}}`, nil
		case 2:
			s.Offer("a", agentrun.Event{ID: "new", Thread: "t", Sender: "s", Text: "只总结重点"})
			return `{"action":"reply","messages":["obsolete draft"]}`, nil
		default:
			joined := ""
			for _, v := range turns {
				joined += v["content"]
			}
			if !strings.Contains(joined, "current_followup") || !strings.Contains(joined, "只总结重点") || !strings.Contains(joined, `"status":"succeeded"`) {
				t.Fatal("feedback/followup missing", joined)
			}
			return `{"action":"reply","messages":["new summary"]}`, nil
		}
	})
	e, err := NewPreview(enabledConfig(), m, "policy")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools = tools
	e.MaxToolCalls = 3
	d, err := e.Respond(agentrun.WithSession(context.Background(), s), Input{Mode: "direct", Preview: true, Text: "hello"})
	if err != nil || d.Action != "reply" || d.Messages[0] != "new summary" || steps != 3 || tools.calls != 1 || !s.Consumed("new") {
		t.Fatal(d, err, steps, tools.calls)
	}
}

type failedRunnerTools struct{ toolFixture }

func (*failedRunnerTools) Call(context.Context, string, json.RawMessage) (string, error) {
	return "", errors.New("unavailable")
}
func TestAgentRunnerReturnsTypedFailureToNextModelStep(t *testing.T) {
	steps := 0
	e, err := NewPreview(enabledConfig(), runnerModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
		steps++
		if steps == 1 {
			return `{"action":"tool","tool":"web_search","arguments":{"query":"hello"}}`, nil
		}
		var feedback struct{ Execution toolExecution }
		if json.Unmarshal([]byte(turns[len(turns)-1]["content"]), &feedback) != nil || feedback.Execution.Status != "failed" || feedback.Execution.Error == "" {
			t.Fatal("tool failure hidden")
		}
		return `{"action":"reply","messages":["暂时连不上"]}`, nil
	}), "policy")
	if err != nil {
		t.Fatal(err)
	}
	e.Tools = &failedRunnerTools{}
	e.MaxToolCalls = 2
	if _, err = e.Respond(context.Background(), Input{Mode: "direct", Preview: true, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
}

type timeoutRunnerTools struct {
	failedRunnerTools
	policyUsed bool
}

func (t *timeoutRunnerTools) ToolTimeout(string) time.Duration {
	t.policyUsed = true
	return time.Millisecond
}
func (t *timeoutRunnerTools) Call(ctx context.Context, _ string, _ json.RawMessage) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

var _ ToolAccess = (*timeoutRunnerTools)(nil)

func TestAdapterDeadlineIsUsedByRunner(t *testing.T) {
	steps := 0
	e, err := NewPreview(enabledConfig(), runnerModelFunc(func(context.Context, string, []map[string]string) (string, error) {
		steps++
		if steps == 1 {
			return `{"action":"tool","tool":"web_search","arguments":{"query":"hello"}}`, nil
		}
		return `{"action":"reply","messages":["暂时连不上"]}`, nil
	}), "policy")
	if err != nil {
		t.Fatal(err)
	}
	tools := &timeoutRunnerTools{}
	e.Tools = tools
	e.MaxToolCalls = 2
	if _, err = e.Respond(context.Background(), Input{Mode: "direct", Preview: true, Text: "hello"}); err != nil || !tools.policyUsed {
		t.Fatal("adapter timeout ignored", err)
	}
}

func TestFollowupChangesResourceContract(t *testing.T) {
	turns := []map[string]string{{"role": "user", "content": `{"kind":"current_message","text":"发一首音乐"}`}, {"role": "user", "content": `{"kind":"current_followup","events":[{"text":"改发原文件，不用语音"}]}`}}
	if need := resourceNeed(turns); need.Kind != "file" || need.Fresh {
		t.Fatal("stale voice contract retained", need)
	}
}
