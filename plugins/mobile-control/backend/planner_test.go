package mobilecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"wechat-bot/internal/mobile"
)

type fakeProvider struct {
	visual       string
	visualErr    error
	answers      []string
	planErr      error
	visualPrompt string
	visualPNG    []byte
	requests     []Request
}

func (f *fakeProvider) DescribeScreen(_ context.Context, prompt string, png []byte) (string, error) {
	f.visualPrompt, f.visualPNG = prompt, append([]byte(nil), png...)
	return f.visual, f.visualErr
}

func (f *fakeProvider) PlanStep(_ context.Context, request Request) (string, error) {
	f.requests = append(f.requests, request)
	if f.planErr != nil {
		return "", f.planErr
	}
	if len(f.answers) == 0 {
		return "", errors.New("unexpected_model_call")
	}
	answer := f.answers[0]
	f.answers = f.answers[1:]
	return answer, nil
}

func toolAnswer(step mobile.Step) string {
	b, _ := json.Marshal(step)
	outer, _ := json.Marshal(map[string]any{"action": "tool", "tool": "device_step", "arguments": json.RawMessage(b)})
	return string(outer)
}

func observation() mobile.Observation {
	return mobile.Observation{
		DeviceID:        "phone-a",
		Package:         "com.example.home",
		AllowedPackages: []string{"com.example.home", "com.browser"},
		Browsers:        []string{"com.browser"},
		PNG:             []byte("synthetic-png"),
		Width:           1080,
		Height:          2400,
		Nodes:           []json.RawMessage{json.RawMessage(`{"text":"synthetic"}`)},
	}
}

func TestPlanUsesPrivateObservationAndPreservesPlanningContract(t *testing.T) {
	p := &fakeProvider{visual: "设置页面，没有敏感弹窗", answers: []string{toolAnswer(mobile.Step{
		Action: "open_url", Summary: "在已允许的浏览器中打开链接", Arguments: map[string]any{"package": "com.browser", "url": "https://example.test/path"},
	})}}
	task := mobile.Task{ID: "task-1", DeviceID: "phone-a", Goal: "打开已授权网页", Artifacts: []mobile.Artifact{{ID: "recording-1"}}}
	got, err := Plan(context.Background(), p, task, observation(), []string{"open_url", "capture"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != "open_url" || got.Arguments["package"] != "com.browser" {
		t.Fatalf("unexpected step: %#v", got)
	}
	if p.visualPrompt != visualPrompt || string(p.visualPNG) != "synthetic-png" {
		t.Fatalf("vision request did not receive current screenshot: prompt=%q png=%q", p.visualPrompt, p.visualPNG)
	}
	if len(p.requests) != 1 || len(p.requests[0].Tools) != 1 {
		t.Fatalf("unexpected model calls: %#v", p.requests)
	}
	req := p.requests[0]
	if !strings.Contains(req.System, "不要登录") || !strings.Contains(req.System, "不能为截图调用capture") || !strings.Contains(req.Tools[0].Description, "RECORD SCREEN VIDEO") {
		t.Fatalf("safety/capture guidance missing: %#v", req)
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(req.Turns[0].Content), &input); err != nil {
		t.Fatal(err)
	}
	if input["visual_observation"] != p.visual || input["current_app"] != "com.example.home" {
		t.Fatalf("observation missing from model input: %#v", input)
	}
	if !strings.Contains(req.Tools[0].Schema, `"minimum":2`) || !strings.Contains(req.Tools[0].Schema, `"open_url"`) {
		t.Fatalf("device tool schema lost capture or URL contract: %s", req.Tools[0].Schema)
	}
}

func TestPlanRepairsMalformedToolCallWithoutPerformingAction(t *testing.T) {
	p := &fakeProvider{visual: "普通界面", answers: []string{
		`{"action":"tool","tool":"device_step","arguments":{"action":"open_url","arguments":{"package":"not-allowed","url":"https://example.test"}}}`,
		toolAnswer(mobile.Step{Action: "wait", Summary: "等待页面加载"}),
	}}
	got, err := Plan(context.Background(), p, mobile.Task{DeviceID: "phone-a", Goal: "等待"}, observation(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != "wait" || len(p.requests) != 2 {
		t.Fatalf("unexpected retry result step=%#v calls=%d", got, len(p.requests))
	}
	if len(p.requests[1].Turns) != 2 || p.requests[1].Turns[1].Content != repairPrompt {
		t.Fatalf("repair instruction missing: %#v", p.requests[1].Turns)
	}
}

func TestPlanRejectsTwoInvalidCalls(t *testing.T) {
	p := &fakeProvider{visual: "普通界面", answers: []string{"{}", "{}"}}
	_, err := Plan(context.Background(), p, mobile.Task{DeviceID: "phone-a"}, observation(), nil)
	if err == nil || err.Error() != "device_model_step_invalid" {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.requests) != 2 {
		t.Fatalf("want exactly two format attempts, got %d", len(p.requests))
	}
}

func TestPlanPropagatesHostProviderError(t *testing.T) {
	want := errors.New("host_model_unavailable")
	p := &fakeProvider{visual: "ordinary", planErr: want}
	_, err := Plan(context.Background(), p, mobile.Task{DeviceID: "phone-a"}, observation(), nil)
	if !errors.Is(err, want) {
		t.Fatalf("want provider error, got %v", err)
	}
}

func TestValidStepEnforcesObservedPackagesAndActionShapes(t *testing.T) {
	o := observation()
	cases := []struct {
		name string
		step mobile.Step
		want bool
	}{
		{"allowed launch", mobile.Step{Action: "launch", Arguments: map[string]any{"package": "com.browser"}}, true},
		{"unknown launch", mobile.Step{Action: "launch", Arguments: map[string]any{"package": "com.other"}}, false},
		{"allowed https browser", mobile.Step{Action: "open_url", Arguments: map[string]any{"package": "com.browser", "url": "https://example.test"}}, true},
		{"http rejected", mobile.Step{Action: "open_url", Arguments: map[string]any{"package": "com.browser", "url": "http://example.test"}}, false},
		{"wrong browser rejected", mobile.Step{Action: "open_url", Arguments: map[string]any{"package": "com.example.home", "url": "https://example.test"}}, false},
		{"valid capture", mobile.Step{Action: "capture", Arguments: map[string]any{"seconds": float64(4)}}, true},
		{"fractional capture rejected", mobile.Step{Action: "capture", Arguments: map[string]any{"seconds": float64(4.5)}}, false},
		{"capture limit rejected", mobile.Step{Action: "capture", Arguments: map[string]any{"seconds": float64(13)}}, false},
		{"swipe coordinates", mobile.Step{Action: "swipe", Arguments: map[string]any{"x": float64(1), "y": float64(2), "x2": float64(3), "y2": float64(4)}}, true},
		{"swipe missing destination", mobile.Step{Action: "swipe", Arguments: map[string]any{"x": float64(1), "y": float64(2)}}, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidStep(tt.step, o); got != tt.want {
				t.Fatalf("ValidStep(%#v)=%v, want %v", tt.step, got, tt.want)
			}
		})
	}
}
