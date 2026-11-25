package station

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

type publicToolAccessFixture struct {
	tools   []mcptools.Tool
	results map[string]string
	trace   companion.Input
	called  []string
}

func (f *publicToolAccessFixture) List(context.Context) ([]mcptools.Tool, error) {
	return append([]mcptools.Tool(nil), f.tools...), nil
}

func (f *publicToolAccessFixture) Call(_ context.Context, name string, _ json.RawMessage) (string, error) {
	f.called = append(f.called, name)
	if value, ok := f.results[name]; ok {
		return value, nil
	}
	return "", errors.New("unknown tool")
}

func (f *publicToolAccessFixture) SetTrace(in companion.Input) { f.trace = in }

func (f *publicToolAccessFixture) ForkTools() companion.ToolAccess {
	copy := *f
	copy.called = nil
	return &copy
}

func (f *publicToolAccessFixture) AutonomousActions() bool { return true }

func (f *publicToolAccessFixture) ToolTimeout(string) time.Duration { return 17 * time.Second }

func TestMergeStationToolAccessKeepsApplicationPrecedenceAndOwnerGate(t *testing.T) {
	primary := &publicToolAccessFixture{
		tools:   []mcptools.Tool{{Name: "wechat_read"}, {Name: "shared"}},
		results: map[string]string{"wechat_read": "app", "shared": "app"},
	}
	public := &publicToolAccessFixture{
		tools:   []mcptools.Tool{{Name: "music_local_start"}, {Name: "shared"}},
		results: map[string]string{"music_local_start": "public", "shared": "public"},
	}
	allowed := false
	merged := mergeStationToolAccess(primary, public, func(context.Context) bool { return allowed })
	merged.(interface{ SetTrace(companion.Input) }).SetTrace(companion.Input{Channel: "wechat", Account: "one", Thread: "room", Sender: "owner"})
	items, err := merged.List(context.Background())
	if err != nil || len(items) != 2 {
		t.Fatalf("unexpected initial tool list: %#v, %v", items, err)
	}
	if _, err := merged.Call(context.Background(), "music_local_start", nil); err == nil {
		t.Fatal("public tool was callable after owner gate was revoked")
	}
	allowed = true
	items, err = merged.List(context.Background())
	if err != nil || len(items) != 3 {
		t.Fatalf("unexpected granted tool list: %#v, %v", items, err)
	}
	if value, err := merged.Call(context.Background(), "shared", nil); err != nil || value != "app" {
		t.Fatalf("application tool did not keep precedence: %q, %v", value, err)
	}
	if value, err := merged.Call(context.Background(), "music_local_start", nil); err != nil || value != "public" {
		t.Fatalf("public tool was not dispatched: %q, %v", value, err)
	}
	if len(primary.called) != 1 || len(public.called) != 1 {
		t.Fatalf("unexpected dispatch records: primary=%v public=%v", primary.called, public.called)
	}
}

func TestMergeStationToolAccessPropagatesTraceAndTimeout(t *testing.T) {
	primary := &publicToolAccessFixture{tools: []mcptools.Tool{{Name: "wechat_read"}}}
	public := &publicToolAccessFixture{tools: []mcptools.Tool{{Name: "web_search"}}}
	merged := mergeStationToolAccess(primary, public, nil)
	in := companion.Input{Channel: "wechat", Account: "one", Thread: "room", Sender: "owner", TraceID: "trace"}
	merged.(interface{ SetTrace(companion.Input) }).SetTrace(in)
	if primary.trace.TraceID != in.TraceID || public.trace.TraceID != in.TraceID {
		t.Fatalf("trace was not propagated: primary=%+v public=%+v", primary.trace, public.trace)
	}
	if _, err := merged.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := merged.(interface{ ToolTimeout(string) time.Duration }).ToolTimeout("web_search"); got != 17*time.Second {
		t.Fatalf("public timeout not selected: %s", got)
	}
	if fork := merged.(interface{ ForkTools() companion.ToolAccess }).ForkTools(); fork == nil {
		t.Fatal("fork returned nil")
	}
}

func TestMergeStationToolAccessUsesCurrentWorkstationDirectoryAtCall(t *testing.T) {
	primary := &publicToolAccessFixture{tools: []mcptools.Tool{{Name: "wechat_read"}}}
	public := &publicToolAccessFixture{
		tools:   []mcptools.Tool{{Name: "project_comfy_run"}},
		results: map[string]string{"project_comfy_run": "started"},
	}
	merged := mergeStationToolAccess(primary, public, nil)
	if _, err := merged.List(t.Context()); err != nil {
		t.Fatal(err)
	}
	public.tools = nil // The workstation disabled the package after discovery.
	if _, err := merged.Call(t.Context(), "project_comfy_run", json.RawMessage(`{}`)); err == nil || len(public.called) != 0 {
		t.Fatalf("disabled workstation tool was dispatched: %v, %v", err, public.called)
	}
	public.tools = []mcptools.Tool{{Name: "project_comfy_run"}}
	if got, err := merged.Call(t.Context(), "project_comfy_run", json.RawMessage(`{}`)); err != nil || got != "started" {
		t.Fatalf("restored workstation tool was unavailable: %q, %v", got, err)
	}
}

func TestPublicToolFactoryUsesCurrentTraceAndForks(t *testing.T) {
	var traces []string
	factory := newPublicToolFactoryAccess(func(in companion.Input) companion.ToolAccess {
		traces = append(traces, in.TraceID)
		return &publicToolAccessFixture{tools: []mcptools.Tool{{Name: "web_search"}}, results: map[string]string{"web_search": in.TraceID}}
	}, func(in companion.Input) bool { return in.Sender == "owner" })
	factory.(interface{ SetTrace(companion.Input) }).SetTrace(companion.Input{Sender: "owner", TraceID: "first"})
	if items, err := factory.List(context.Background()); err != nil || len(items) != 1 {
		t.Fatalf("factory did not expose the granted tool: %#v, %v", items, err)
	}
	if got, err := factory.Call(context.Background(), "web_search", nil); err != nil || got != "first" {
		t.Fatalf("factory call used wrong trace: %q, %v", got, err)
	}
	fork := factory.(interface{ ForkTools() companion.ToolAccess }).ForkTools()
	fork.(interface{ SetTrace(companion.Input) }).SetTrace(companion.Input{Sender: "member", TraceID: "blocked"})
	if items, err := fork.List(context.Background()); err != nil || len(items) != 0 {
		t.Fatalf("factory leaked tools after owner gate: %#v, %v", items, err)
	}
	if len(traces) != 1 {
		t.Fatalf("unexpected factory scope creations: %v", traces)
	}
}
