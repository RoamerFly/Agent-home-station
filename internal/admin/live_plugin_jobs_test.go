package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

type isolatedPluginJobTools struct {
	p     *permittedTools
	calls []string
}

func (t *isolatedPluginJobTools) SetTrace(in companion.Input) { t.p.SetTrace(in) }
func (t *isolatedPluginJobTools) List(ctx context.Context) ([]mcptools.Tool, error) {
	all, err := t.p.List(ctx)
	out := []mcptools.Tool{}
	for _, tool := range all {
		if tool.Name == "demo__create" || tool.Name == "plugin_job_status" || tool.Name == "plugin_job_cancel" {
			out = append(out, tool)
		}
	}
	return out, err
}
func (t *isolatedPluginJobTools) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if name != "demo__create" && name != "plugin_job_status" && name != "plugin_job_cancel" {
		return "", errors.New("isolated tool not available")
	}
	t.calls = append(t.calls, name)
	return t.p.Call(ctx, name, args)
}
func (t *isolatedPluginJobTools) Media(id string) (companion.GeneratedMedia, bool) {
	return t.p.Media(id)
}

func TestLivePluginBackgroundModelFlowWithoutWeChatSend(t *testing.T) {
	root, workspace := os.Getenv("WECHAT_LIVE_MEDIA_ROOT"), os.Getenv("WECHAT_LIVE_WORKSPACE")
	if root == "" || os.Getenv("WECHAT_PLUGIN_JOBS_LIVE") != "1" {
		t.Skip("explicit isolated real-model evaluation")
	}
	c, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	j := newJobFixture(t)
	provider, err := j.f.manager.makeModel(c)
	if err != nil {
		t.Fatal(err)
	}
	cfg := j.f.manager.config.Companion
	engine, err := companion.New(cfg, j.f.manager.memory, provider, c.SystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	tools := &isolatedPluginJobTools{p: j.p}
	engine.Tools, engine.MaxToolCalls = tools, 8
	in := j.p.trace
	in.Text = "请用演示异步创作服务生成一条鲸鱼晚安短句（prompt=whale goodnight），由服务完成，不要自己直接编写；接下任务后等后台结果。"
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	initial, err := engine.Respond(ctx, in)
	cancel()
	if err != nil || j.creates.Load() != 1 {
		t.Fatal("model did not create exactly one job", err, j.creates.Load(), tools.calls)
	}
	j.done.Store(true)
	resultInput, err := j.f.manager.claimAgentTask(time.Now().Add(3*time.Second), func(string) bool { return false })
	if err != nil || resultInput == nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 90*time.Second)
	final, err := engine.Respond(ctx, *resultInput)
	cancel()
	if err != nil || final.Action != "reply" || j.creates.Load() != 1 {
		t.Fatal("completion did not retrieve original job", err, final, j.creates.Load())
	}
	seen := false
	for _, name := range tools.calls {
		seen = seen || name == "plugin_job_status"
	}
	if !seen {
		t.Fatal("model skipped actual completed output", tools.calls)
	}
	if workspace != "" {
		b, _ := json.MarshalIndent(map[string]any{"initial": initial, "final": final, "calls": tools.calls, "create_requests": j.creates.Load(), "poll_requests": j.polls.Load(), "wechat_sends": 0, "plugin_provider": "local synthetic asynchronous HTTP service; real LLM decisions"}, "", "  ")
		if err := os.WriteFile(filepath.Join(workspace, "research/evidence/plugin-jobs-real-model.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
