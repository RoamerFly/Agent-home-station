package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"wechat-bot/internal/agentrun"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

type backgroundCapabilityProbe struct {
	count     int
	reports   []companion.ContextReport
	followups []agentrun.Event
}

func (*backgroundCapabilityProbe) AutonomousActions() bool           { return true }
func (p *backgroundCapabilityProbe) ForkTools() companion.ToolAccess { return p }
func (p *backgroundCapabilityProbe) ContextPrepared(r companion.ContextReport) {
	p.reports = append(p.reports, r)
}
func (p *backgroundCapabilityProbe) ObserveFollowups(e []agentrun.Event) {
	p.followups = append(p.followups, e...)
}
func (*backgroundCapabilityProbe) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "server_read", InputSchema: json.RawMessage(`{"type":"object","properties":{"page":{"type":"integer"}},"required":["page"]}`)}}, nil
}
func (p *backgroundCapabilityProbe) Call(context.Context, string, json.RawMessage) (string, error) {
	p.count++
	return `{"state":"read"}`, nil
}

type backgroundCapabilityModel struct{ n int }

func (m *backgroundCapabilityModel) Chat(context.Context, string, []map[string]string) (string, error) {
	m.n++
	if m.n <= 35 {
		return fmt.Sprintf(`{"action":"tool","tool":"server_read","arguments":{"page":%d}}`, m.n), nil
	}
	return `{"action":"reply","messages":["模型完成了合成测试"]}`, nil
}

func TestBackgroundToolsPreserveAutonomyContextAndFollowups(t *testing.T) {
	probe := &backgroundCapabilityProbe{}
	w, c, n := backgroundWorker(t, &backgroundCapabilityModel{})
	c.Tools = probe
	c.MaxToolCalls = 1
	w.enableGenerations()
	defer w.closeGenerations()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "capabilities", Mode: "direct", Text: "读取合成分页资料"}
	w.startGeneration(context.Background(), c, in, nil)
	if len(w.generation.jobs) != 1 {
		t.Fatal("generation did not start")
	}
	pumpGeneration(t, w, func() bool { return len(w.generation.jobs) == 0 })
	readyPlans(w)
	w.reconcileUtterancePlans(context.Background())
	if probe.count != 35 || len(probe.reports) != 1 || !probe.reports[0].HasCurrent || len(n.submissions) != 1 {
		t.Fatal("background wrapper dropped a capability", probe.count, probe.reports, n.submissions)
	}
	wrapped := &generationTools{inner: probe}
	event := agentrun.Event{ID: "new", Text: "换成女声", Thread: in.Thread, Sender: in.Sender, Kind: 1}
	wrapped.ObserveFollowups([]agentrun.Event{event})
	if len(probe.followups) != 1 || probe.followups[0].ID != "new" {
		t.Fatal("followup observer was not forwarded")
	}
}
