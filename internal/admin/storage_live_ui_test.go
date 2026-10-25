package admin

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

// This opt-in server uses only synthetic records and a fake native adapter.
// No real WeChat session, provider credential or message sender is opened.
func TestStorageUIFixture(t *testing.T) {
	workspace := os.Getenv("WECHAT_STORAGE_UI_FIXTURE")
	if workspace == "" {
		t.Skip("explicit isolated browser fixture")
	}
	f := setup(t)
	c := f.manager.config.Companion
	c.Permissions.Remember = true
	c.MemoryMessages = 20
	f.manager.config.Companion = c
	now := time.Now()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "synthetic-alice"}
	for i := 0; i < 23; i++ {
		in.TraceID = fmt.Sprintf("synthetic-%d", i)
		in.Text = fmt.Sprintf("合成归档消息 %d", i)
		if err := f.manager.memory.Observe(in, c, now); err != nil {
			t.Fatal(err)
		}
	}
	f.manager.journal = []LogEntry{{SchemaVersion: 2, ID: "synthetic-old", Time: now.Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339Nano), Source: "合成测试群", SourceKind: "group", Content: "合成旧日志：不会重发到微信", Event: "message_received", Category: "received"}}
	f.manager.record(bot.Record{Event: "message_received", Content: "合成当前日志", ThreadID: "filehelper"})
	stop := filepath.Join(workspace, "research/evidence/storage-ui-stop")
	if _, err := os.Stat(stop); err == nil {
		t.Fatal("remove the previous fixture stop marker before starting")
	}
	if err := os.WriteFile(filepath.Join(workspace, "research/evidence/storage-ui-url.txt"), []byte(f.server.URL), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("Synthetic storage UI:", f.server.URL)
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(stop); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("browser fixture expired before controlled shutdown")
}
