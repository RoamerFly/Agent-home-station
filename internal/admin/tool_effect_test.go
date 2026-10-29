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
