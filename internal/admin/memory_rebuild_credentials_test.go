package admin

import (
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestExplicitMemoryRebuildUsesPrivateRuntimeCredential(t *testing.T) {
	f := setup(t)
	f.manager.mu.Lock()
	f.manager.config.APIKey = "synthetic-maintenance-key"
	f.manager.config.Provider = "openai-compatible"
	// makeModel persists the private runtime key before validating the URL.
	// This deliberately invalid endpoint proves the internal path retained the
	// credential without opening a network connection or weakening HTTPS policy.
	f.manager.config.BaseURL = "synthetic-invalid-endpoint"
	f.manager.config.Model = "synthetic"
	f.manager.config.Companion.Enabled = true
	f.manager.config.Companion.Permissions.Remember = true
	cfg := cloneConfig(f.manager.config)
	f.manager.mu.Unlock()
	if f.manager.Config().APIKey != "" {
		t.Fatal("public config exposed credential")
	}
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "synthetic@chatroom", Sender: "synthetic-user", TraceID: "synthetic-rebuild", Text: "我喜欢红茶。"}
	if err := f.manager.memory.Observe(in, cfg.Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.startMemoryRebuild(companion.ScopeID(in.Channel, in.Account, in.Thread)); err != nil {
		t.Fatal(err)
	}
	f.manager.memoryRunnerWG.Wait()
	key, err := os.ReadFile(filepath.Join(f.manager.dir, "provider-key"))
	if err != nil || string(key) != "synthetic-maintenance-key" {
		t.Fatal("private runtime credential never reached provider initialization", err)
	}
}
