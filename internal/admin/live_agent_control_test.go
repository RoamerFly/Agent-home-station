package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

// Opt-in: the real configured model, but an isolated manager, temporary files,
// fake WeChat transport and NO granted messaging capability.
func TestLiveAgentHostAndPersistentCheckpoint(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || workspace == "" || os.Getenv("WECHAT_AGENT_LIVE") != "1" {
		t.Skip("explicit isolated Agent host diagnostic")
	}
	source, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	f, p := trustedAgent(t)
	c := f.manager.agent.config()
	c.Host.Mode = "full"
	if err = c.validate(); err != nil {
		t.Fatal(err)
	}
	if err = f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	f.manager.mu.Lock()
	f.manager.mcp.Enabled = false
	f.manager.mu.Unlock()
	p, _, err = f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	provider, err := f.manager.makeModel(source)
	if err != nil {
		t.Fatal(err)
	}
	cfg := source.Companion
	cfg.Enabled = true
	cfg.QuietStart = "00:00"
	cfg.QuietEnd = "00:00"
	cfg.Permissions.Remember = true
	cfg.Permissions.ReplyText = true
	cfg.Conversation = companion.ConversationDefaults()
	cfg.MaxBubbles = 3
	engine, err := companion.New(cfg, f.manager.memory, provider, source.SystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	engine.Tools = p
	engine.MaxToolCalls = 12
	path := filepath.Join(c.Host.Roots[0], "agent-probe.txt")
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", Mode: "direct", Directed: true, TraceID: "isolated-agent-host", Text: "这是已授权的服务器能力隔离验收。不要联网、不要发微信消息。请用server_write在" + path + "创建内容为agent-host-probe-ok的新文件；用server_exec在目录" + c.Host.Roots[0] + "执行命令 printf agent-host-probe-ok。最后用agent_task保存completed任务，key=host-probe，title=服务器能力验收，checkpoint中记下实际命令输出和文件结果。不要只回答文字，必须真实执行这三个动作。"}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	d, err := engine.Respond(ctx, in)
	data, readErr := os.ReadFile(path)
	tasks := f.manager.agent.list("test", "wxid_owner")
	ops := f.manager.agent.operations()
	report := map[string]any{"decision": d, "model_error": err != nil, "file_created": readErr == nil && string(data) == "agent-host-probe-ok", "completed_checkpoint": len(tasks) == 1 && tasks[0].State == "completed", "host_operation_count": len(ops), "wechat_sends": 0, "isolated_host_directory": true, "production_permissions_changed": false}
	b, _ := json.MarshalIndent(report, "", "  ")
	if saveErr := os.WriteFile(filepath.Join(workspace, "research/evidence/agent-upgrade-live-host.json"), b, 0600); saveErr != nil {
		t.Fatal(saveErr)
	}
	if err != nil || readErr != nil || string(data) != "agent-host-probe-ok" || len(tasks) != 1 || tasks[0].State != "completed" || len(ops) != 2 {
		t.Fatal("real Agent did not complete host/checkpoint actions", d, err, readErr, len(tasks), len(ops))
	}
}
