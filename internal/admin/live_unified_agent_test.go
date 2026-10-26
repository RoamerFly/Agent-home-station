package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
)

// Real configured model, isolated project and fake WeChat. No production
// permissions, real conversation contents or source project files are changed.
func TestLiveUnifiedAgentReadsRepairsExecutesAndRemembers(t *testing.T) {
	root := os.Getenv("WECHAT_LIVE_MEDIA_ROOT")
	if root == "" || os.Getenv("WECHAT_UNIFIED_AGENT_LIVE") != "1" {
		t.Skip("explicit live model with isolated synthetic project only")
	}
	live, e := loadConfig(filepath.Join(root, "admin"))
	if e != nil {
		t.Fatal(e)
	}
	f, p := trustedAgent(t)
	cfg := f.manager.agent.config()
	cfg.Host.Mode = "full"
	cfg.MaxSteps = 30
	if e = f.manager.agent.save(cfg); e != nil {
		t.Fatal(e)
	}
	project := cfg.Host.Roots[0]
	os.WriteFile(filepath.Join(project, "README.md"), []byte("# Synthetic calculation project\nRun python3 check.py. Target result is 42. Change only calculation.py; keep check.py unchanged.\n"), 0600)
	os.WriteFile(filepath.Join(project, "calculation.py"), []byte("def result():\n    return 21\n"), 0600)
	os.WriteFile(filepath.Join(project, "check.py"), []byte("from calculation import result\nassert result() == 42, 'expected 42'\nprint('verified-42')\n"), 0600)
	c := f.manager.config
	c.Companion.Enabled = true
	c.Companion.Permissions.Remember = true
	c.Companion.QuietStart = "00:00"
	c.Companion.QuietEnd = "00:00"
	f.manager.config = c
	key := filepath.Join(f.dir, "live-key")
	os.WriteFile(key, []byte(live.APIKey), 0600)
	model, e := deepseek.New(deepseek.Config{BaseURL: live.BaseURL, Model: live.Model, KeyFile: key, Compatible: live.Provider == "openai-compatible", MaxTokens: 4096})
	if e != nil {
		t.Fatal(e)
	}
	engine, e := companion.New(c.Companion, f.manager.memory, model, c.SystemPrompt)
	if e != nil {
		t.Fatal(e)
	}
	engine.Tools = p
	engine.MaxToolCalls = 30
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "isolated-repair", Mode: "direct", Directed: true, Text: "在已授权项目目录 " + project + " 中读取README和代码，修复错误并运行python3 check.py验证。只修改calculation.py，不能改测试。用server_read返回的sha256进行server_edit修改。使用短命令server_exec验证即可。最后把实际验证结果和使用方法写入本会话MEMORY.md（先读revision）。不要发送微信消息，不要修改项目以外的文件。"}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	decision, e := engine.Respond(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	v, e := p.Call(ctx, "server_exec", json.RawMessage(`{"path":"`+filepath.ToSlash(project)+`","command":"python3 check.py"}`))
	if e != nil {
		t.Fatal(e)
	}
	var result struct {
		ExitCode int `json:"exit_code"`
		Output   string
	}
	json.Unmarshal([]byte(v), &result)
	if result.ExitCode != 0 {
		t.Fatal(result)
	}
	reopened, e := f.manager.workspace.Read(companion.ScopeID("wechat", "test", "wxid_owner"), "MEMORY.md")
	if e != nil || reopened.Revision == "" {
		t.Fatal("memory was not saved", e)
	}
	report := map[string]any{"real_model": true, "synthetic_project": true, "verified_exit_code": result.ExitCode, "verification_output": result.Output, "memory_saved": true, "decision": decision.Action, "wechat_sends": 0}
	target := os.Getenv("WECHAT_UNIFIED_REPORT")
	if target != "" {
		b, _ := json.MarshalIndent(report, "", "  ")
		os.WriteFile(target, b, 0600)
	}
}
