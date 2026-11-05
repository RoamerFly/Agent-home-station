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

func TestLiveReceivedPublicVideoObservation(t *testing.T) {
	root, messagePath, output := os.Getenv("WECHAT_SHARE_LIVE_ROOT"), os.Getenv("WECHAT_SHARE_LIVE_MESSAGE"), os.Getenv("WECHAT_SHARE_LIVE_OUTPUT")
	if root == "" || messagePath == "" || output == "" {
		t.Skip("explicit received-share diagnostic only; no sends")
	}
	c, err := loadConfig(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal("configuration unavailable")
	}
	var m linuxnative.Message
	b, err := os.ReadFile(messagePath)
	if err != nil || json.Unmarshal(b, &m) != nil || m.Type != 49 || !permittedRecipient(c, m.ThreadID) {
		t.Fatal("received share unavailable or scope denied")
	}
	native, err := linuxnative.NewAdmin(linuxnative.Config{URL: "http://127.0.0.1:6174", TokenFile: filepath.Join(root, "api-token"), Account: m.AccountID})
	if err != nil {
		t.Fatal("native unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	actual, err := native.Message(ctx, m.ID)
	if err != nil || actual.ThreadID != m.ThreadID || actual.Link == nil {
		t.Fatal("original share mismatch")
	}
	f := setup(t)
	f.manager.config = c
	f.manager.native = native
	legacySpeechSource, err := loadSpeech(filepath.Join(root, "admin"))
	if err == nil {
		writeLegacySpeechMigrationFixture(t, f.manager, legacySpeechSource)
	}
	if err != nil {
		t.Fatal("speech configuration unavailable")
	}
	text, err := (mediaUnderstanding{manager: f.manager}).Analyze(ctx, actual)
	if err != nil || !strings.Contains(text, "[微信视频实际内容观察") {
		t.Fatal("actual video observation failed", sharedVideoReadReason(err))
	}
	assets, err := f.manager.loadAssets()
	if err != nil || len(assets) != 1 {
		t.Fatal("original video not retained")
	}
	report := map[string]any{"observation": text, "message": actual, "video_bytes": assets[0].Bytes, "wechat_sends": 0, "actual_visual": strings.Contains(text, "[画面观察"), "actual_audio": strings.Contains(text, "[实际音轨转写"), "cached_original": true}
	b, _ = json.MarshalIndent(report, "", "  ")
	if os.WriteFile(output, b, 0600) != nil {
		t.Fatal("private report write failed")
	}
}
