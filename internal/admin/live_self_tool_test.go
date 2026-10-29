package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

type selfToolProbe struct {
	p         *permittedTools
	calls     map[string]int
	failedRun bool
}

func (s *selfToolProbe) SetTrace(in companion.Input)             { s.p.SetTrace(in) }
func (s *selfToolProbe) AgentContext(ctx context.Context) string { return s.p.AgentContext(ctx) }
func (s *selfToolProbe) List(ctx context.Context) ([]mcptools.Tool, error) {
	all, e := s.p.List(ctx)
	out := []mcptools.Tool{}
	for _, v := range all {
		switch v.Name {
		case "server_status", "server_list", "server_read", "server_write", "server_edit", "server_exec", "workspace_read", "workspace_write":
			out = append(out, v)
		}
	}
	return out, e
}
func (s *selfToolProbe) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	s.calls[name]++
	v, e := s.p.Call(ctx, name, args)
	if name == "server_exec" {
		var out struct {
			ExitCode int `json:"exit_code"`
		}
		if json.Unmarshal([]byte(v), &out) == nil && out.ExitCode != 0 {
			s.failedRun = true
		}
		if e != nil || strings.Contains(v, "AssertionError") {
			s.failedRun = true
		}
	}
	return v, e
}

// Opt-in real model, real isolated files/processes. The model only receives
// primitive file/exec tools; no prewritten business conversion wrapper.
func TestLiveAgentWritesOwnToolRepairsAndRuns(t *testing.T) {
	root := os.Getenv("WECHAT_LIVE_MEDIA_ROOT")
	if root == "" || os.Getenv("WECHAT_SELF_TOOL_LIVE") != "1" {
		t.Skip("explicit isolated model self-tool check")
	}
	live, e := loadConfig(filepath.Join(root, "admin"))
	if e != nil {
		t.Fatal(e)
	}
	f, p := trustedAgent(t)
	cfg := f.manager.agent.config()
	cfg.Host.Mode = "full"
	if e = f.manager.agent.save(cfg); e != nil {
		t.Fatal(e)
	}
	project := p.executionContext().Conversation
	if e = os.WriteFile(filepath.Join(project, "duration.py"), []byte("def duration(samples,rate):\n    return samples * rate\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(project, "check.py"), []byte("from duration import duration\nassert duration(48000,24000)==2, 'duration must be samples / rate'\nprint('verified-duration-2')\n"), 0600); e != nil {
		t.Fatal(e)
	}
	model, e := f.manager.makeModel(live)
	if e != nil {
		t.Fatal(e)
	}
	c := live.Companion
	c.Enabled = true
	c.QuietStart = "00:00"
	c.QuietEnd = "00:00"
	c.Permissions.Remember = true
	engine, e := companion.New(c, f.manager.memory, model, live.SystemPrompt)
	if e != nil {
		t.Fatal(e)
	}
	probe := &selfToolProbe{p: p, calls: map[string]int{}}
	engine.Tools = probe
	engine.MaxToolCalls = agentTurnToolLimit
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	decision, e := engine.Respond(ctx, companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "write-own-tool", Mode: "direct", Directed: true, Text: "这是合成项目隔离验收。当前项目是 " + project + "。先实际运行 python3 check.py 复现错误，然后读取并修复 duration.py；check.py不能修改。自己编写一个新工具 audio_duration.py，采用命令行参数 --samples 与 --rate，调用修复后的函数，输出JSON，例如48000和24000得到{\"seconds\":2}。运行测试和新工具，读取真实结果，并创建 TOOL.md 保存使用方法和本次验证。不得联网、发送微信或操作此项目外资料。不要仅写执行计划。"})
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("decision=%s messages=%v calls=%v", decision.Action, decision.Messages, probe.calls)
	args, _ := json.Marshal(map[string]string{"path": project, "command": "python3 check.py && python3 audio_duration.py --samples 48000 --rate 24000"})
	v, e := p.Call(ctx, "server_exec", args)
	if e != nil {
		t.Fatal(e)
	}
	var out struct {
		ExitCode int `json:"exit_code"`
		Output   string
	}
	if json.Unmarshal([]byte(v), &out) != nil || out.ExitCode != 0 || !strings.Contains(out.Output, "verified-duration-2") {
		t.Fatal("independent tool execution failed", v)
	}
	if _, e = os.Stat(filepath.Join(project, "TOOL.md")); e != nil {
		t.Fatal(e)
	}
	if probe.calls["server_edit"] == 0 || probe.calls["server_write"] < 2 || !probe.failedRun {
		t.Fatal("missing write/error/repair evidence", probe.calls, probe.failedRun)
	}
	if path := os.Getenv("WECHAT_SELF_TOOL_REPORT"); path != "" {
		b, _ := json.MarshalIndent(map[string]any{"real_model": true, "isolated_files_and_processes": true, "calls": probe.calls, "observed_failed_execution": probe.failedRun, "independent_exit_code": out.ExitCode, "output": out.Output, "decision": decision.Action, "wechat_sends": 0}, "", "  ")
		if e = os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
}
