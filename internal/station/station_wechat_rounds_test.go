package station

import (
	"os"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/bot"
	"wechat-bot/internal/workspacefs"
)

func TestWechatExecutionRoundsKeepAccountAndDateBoundaries(t *testing.T) {
	dir := t.TempDir()
	worker := &stationWechatWorker{dir: dir}
	timestamp := time.Date(2026, 11, 9, 16, 1, 0, 0, time.UTC)
	entry := bot.Record{Event: "message_received", ThreadID: "synthetic-group", MessageID: "synthetic-message",
		Category: "received", Status: "received", Content: "synthetic excluded body", TargetURL: "https://example.invalid/"}
	if err := worker.recordRound("synthetic-connection", "synthetic-account", timestamp, entry); err != nil {
		t.Fatal(err)
	}
	entry.Event, entry.Status, entry.SendID = "message_sent", "accepted", "synthetic-receipt"
	if err := worker.recordRound("synthetic-connection", "synthetic-account", timestamp.Add(time.Second), entry); err != nil {
		t.Fatal(err)
	}
	root, err := workspacefs.OpenPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	date := "2026-11-10"
	name := wechatRoundFile("synthetic-connection", "synthetic-account", date)
	day, err := readWechatRoundDay(root, name)
	if err != nil || len(day.Steps) != 2 || day.Steps[1].Sequence != 2 || day.Steps[1].Receipt != "synthetic-receipt" {
		t.Fatalf("round journal: %+v %v", day, err)
	}
	for _, name := range []string{
		wechatRoundFile("synthetic-other-connection", "synthetic-account", date),
		wechatRoundFile("synthetic-connection", "synthetic-other-account", date),
		wechatRoundFile("synthetic-connection", "synthetic-account", "2026-11-09"),
	} {
		other, err := readWechatRoundDay(root, name)
		if err != nil || len(other.Steps) != 0 {
			t.Fatalf("journal escaped connection, account or date: %+v %v", other, err)
		}
	}
	data, err := root.ReadFile(name)
	if err != nil || strings.Contains(string(data), entry.Content) || strings.Contains(string(data), entry.TargetURL) {
		t.Fatalf("unexpected raw activity content: %v", err)
	}
	if err := root.WriteFile(name, []byte("invalid synthetic journal"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := worker.recordRound("synthetic-connection", "synthetic-account", timestamp, entry); err == nil {
		t.Fatal("corrupt history silently replaced")
	}
	if raw, err := os.ReadFile(root.Name() + string(os.PathSeparator) + name); err != nil || string(raw) != "invalid synthetic journal" {
		t.Fatal("corrupt history was modified", err)
	}
}
