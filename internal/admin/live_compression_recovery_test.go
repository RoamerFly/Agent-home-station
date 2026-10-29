package admin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
)

type compressionChatOnly struct{ client *deepseek.Client }

func (m compressionChatOnly) Chat(ctx context.Context, system string, turns []map[string]string) (string, error) {
	return m.client.Chat(ctx, system, turns)
}

// Explicit diagnosis uses a private copy and the existing configured provider.
// It cannot write live memory, run tools, or send any WeChat message.
func TestLiveCompressionRecoveryOnPrivateCopy(t *testing.T) {
	mode, root := os.Getenv("WECHAT_CONTEXT_RECOVERY_LIVE"), os.Getenv("WECHAT_LIVE_MEDIA_ROOT")
	if root == "" || mode != "before" && mode != "after" {
		t.Skip("explicit private-copy compression diagnosis")
	}
	c, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal("config unavailable")
	}
	f := setup(t)
	provider, err := f.manager.makeModel(c)
	if err != nil {
		t.Fatal("provider unavailable")
	}
	data, err := os.ReadFile(filepath.Join(root, "admin", "companion", "memory.json"))
	if err != nil {
		t.Fatal("private memory unavailable")
	}
	dir := t.TempDir()
	if os.WriteFile(filepath.Join(dir, "memory.json"), data, 0600) != nil {
		t.Fatal("private copy unavailable")
	}
	store, err := companion.NewStore(dir)
	if opened := store; opened != nil { t.Cleanup(func(){ _ = opened.Close() }) }
	if err != nil {
		t.Fatal("copy invalid")
	}
	var selected companion.Scope
	for _, row := range store.Summaries(c.Companion, time.Now()) {
		scope, _ := store.Read(row.ID, c.Companion, time.Now())
		remaining := len(scope.Messages) - scope.CompressedCount
		if scope.Channel == "wechat" && remaining > len(selected.Messages)-selected.CompressedCount {
			selected = scope
		}
	}
	if len(selected.Messages)-selected.CompressedCount <= c.Companion.ContextConfig().RecentMessages {
		t.Skip("no compressible copied scope")
	}
	var model companion.LanguageModel = provider
	if mode == "before" {
		model = compressionChatOnly{provider}
	}
	engine, err := companion.New(c.Companion, store, model, c.SystemPrompt)
	if err != nil {
		t.Fatal("engine unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	changed, failure := engine.CompressContext(ctx, companion.Input{Channel: selected.Channel, Account: selected.Account, Thread: selected.Thread}, true)
	if mode == "before" {
		code := "none"
		if diagnostic, ok := deepseek.ErrorDiagnostics(failure); ok {
			code = diagnostic.Code
		} else if failure != nil {
			code = "summary_validation_or_snapshot_conflict"
		}
		t.Logf("diagnosis ordinary_reply_tokens=%d changed=%v failure_code=%s live_memory_writes=0 wechat_sends=0", c.MaxTokens, changed, code)
		return
	}
	if failure != nil || !changed {
		if d, ok := deepseek.ErrorDiagnostics(failure); ok {
			t.Fatalf("compression still failed: %s", d.Code)
		}
		t.Fatal("compression schema or snapshot failure")
	}
	t.Log("private-copy compression succeeded; live memory writes=0; wechat sends=0")
}
