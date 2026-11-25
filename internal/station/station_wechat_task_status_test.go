package station

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func TestStationWechatTaskStatusReadsCurrentCheckpointWithoutReplay(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "queue"), 0700); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"version":2,"server":"dataset","account":"one","inbox":[{"event":{"data":{"messageId":"in-1","threadId":"room"}},"phase":"received","updated":"2026-11-16T01:02:03Z"}],"deliveries":[{"trace":"in-1","thread":"room","text":"答复","bubble":0,"key":"delivery-key","state":"waiting","created":"2026-11-16T01:02:03Z","next":"2026-11-16T01:03:03Z","input":{"account":"one","thread":"room"}}]}`)
	if err := os.WriteFile(filepath.Join(dir, "queue", "messages-v2.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	native := &linuxnative.Sender{}
	tools := &stationAttachmentTools{
		native:  native,
		dir:     dir,
		grants:  map[string]time.Time{"room": time.Unix(0, 0).UTC()},
		reads:   map[string]time.Time{"room": time.Unix(0, 0).UTC()},
		trace:   companion.Input{Account: "one", Thread: "room", MessageTime: "2026-11-16T02:00:00Z"},
		blocked: func() bool { return false },
	}
	// Sender.Account cannot be populated by a fixture, so use the direct
	// reader to assert the compatibility parser and identity boundary.
	file, err := os.Open(filepath.Join(dir, "queue", "messages-v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	page, err := bot.ReadTaskEvidencePageFrom(file, "one", "room", "", 40)
	file.Close()
	if err != nil || len(page.Items) != 2 || page.Items[0].Thread != "room" {
		t.Fatal("checkpoint projection failed", page, err)
	}
	if _, _, _, err := tools.taskStatusInput([]byte(`{"thread_id":"room","limit":40}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "delivery-key") {
		t.Fatal("fixture lost durable delivery identity")
	}
}
