package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/linuxnative"
)

// Explicit owner-requested diagnostic only. No WeChat send interface is used;
// ordinary CI neither opens private messages nor bills the configured model.
func TestLiveReceivedStickerDiagnostic(t *testing.T) {
	root, messagePath, output := os.Getenv("WECHAT_STICKER_LIVE_ROOT"), os.Getenv("WECHAT_STICKER_LIVE_MESSAGE"), os.Getenv("WECHAT_STICKER_LIVE_OUTPUT")
	if root == "" || messagePath == "" || output == "" {
		t.Skip("explicit local diagnostic only")
	}
	c, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal("private configuration unavailable")
	}
	var original linuxnative.Message
	b, err := os.ReadFile(messagePath)
	if err != nil || json.Unmarshal(b, &original) != nil {
		t.Fatal("diagnostic message unavailable")
	}
	allowed := false
	for _, id := range c.allowed() {
		allowed = allowed || id == original.ThreadID
	}
	if !allowed || original.Type != 47 || !c.Behavior.ReadImages {
		t.Fatal("diagnostic scope or permission denied")
	}
	native, err := linuxnative.NewAdmin(linuxnative.Config{URL: "http://127.0.0.1:6174", TokenFile: filepath.Join(root, "api-token"), Account: original.AccountID})
	if err != nil {
		t.Fatal("native client unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	m, err := native.Message(ctx, original.ID)
	if err != nil || m.Type != 47 || m.ThreadID != original.ThreadID || m.SenderID != original.SenderID {
		t.Fatal("received sticker identity mismatch")
	}
	f := setup(t)
	f.manager.config = c
	f.manager.native = native
	text, err := (mediaUnderstanding{manager: f.manager}).Analyze(ctx, m)
	if err != nil || !strings.HasPrefix(text, "[收到表情包的视觉观察") {
		t.Fatal("real sticker observation failed", err)
	}
	report := map[string]any{"message": m, "observation": text, "real_attachment_read": true, "real_configured_vision": true, "wechat_sends": 0, "production_memory_modified": false}
	b, _ = json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(output, b, 0600); err != nil {
		t.Fatal("private report unavailable")
	}
}
