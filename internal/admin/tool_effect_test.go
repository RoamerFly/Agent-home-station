package admin

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"wechat-bot/internal/companion"
)

func TestToolEffectUsesScopedRelativePathOnly(t *testing.T) {
	f := setup(t)
	p := &permittedTools{manager: f.manager, trace: companion.Input{Account: f.manager.native.Account(), Thread: "filehelper"}}
	root := p.executionContext().Conversation
	inside := filepath.Join(root, "output", "result.txt")
	args, _ := json.Marshal(map[string]string{"path": inside, "content": "private source text"})
	got := p.toolEffect("server_write", args, `{}`)
	if got != "已创建文件：会话根目录/output/result.txt" || strings.Contains(got, root) || strings.Contains(got, "private source text") {
		t.Fatal(got)
	}
	outside, _ := json.Marshal(map[string]string{"path": filepath.Join(t.TempDir(), "other.txt")})
	if got := p.toolEffect("server_write", outside, `{}`); got != "" {
		t.Fatal("outside path disclosed:", got)
	}
}

func TestToolArgsSummaryKeepsUsefulParametersWithoutPayload(t *testing.T) {
	got := toolArgsSummary(json.RawMessage(`{"plugin":"comfyui","capability":"image.generate","arguments":{"model":"sample-model","width":1024,"seed":42,"prompt":"private prompt","api_key":"secret"}}`))
	for _, expected := range []string{"plugin=comfyui", "capability=image.generate", "model=sample-model", "width=1024", "seed=42"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("missing %q in %q", expected, got)
		}
	}
	if strings.Contains(got, "private prompt") || strings.Contains(got, "secret") {
		t.Fatal("tool payload leaked into activity summary")
	}
}
