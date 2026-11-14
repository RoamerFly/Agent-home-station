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
	if day.Steps[0].ToolTrackingVersion != 1 || day.Steps[1].ToolTrackingVersion != 0 {
		t.Fatal("tracking coverage was claimed for the wrong step", day.Steps)
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

func TestWechatExecutionRoundKeepsEveryToolAttemptWithoutRawPayloads(t *testing.T) {
	dir := t.TempDir()
	worker := &stationWechatWorker{dir: dir}
	now := time.Now().UTC()
	entry := bot.Record{Event: "tool_started", Category: "tool", Status: "running", ThreadID: "synthetic-room",
		MessageID: strings.Repeat("native-message", 60), CallID: "synthetic-call", Tool: "web_search", ToolAttempt: 1,
		ToolStage: "invocation", Content: "private tool result", Query: "private query", TargetURL: "https://private.invalid/"}
	for index, event := range []string{"tool_started", "tool_failed", "tool_retry_scheduled", "tool_started", "tool_completed"} {
		entry.Event = event
		if index >= 3 {
			entry.ToolAttempt, entry.ToolRetries = 2, 1
		}
		entry.DurationMS, entry.ToolRequests, entry.ToolHTTPStatus, entry.ToolCode = 123, 2, 503, "http_status"
		if err := worker.recordRound("synthetic-connection", "synthetic-account", now, entry); err != nil {
			t.Fatal(err)
		}
	}
	root, err := workspacefs.OpenPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	day, err := readWechatRoundDay(root, wechatRoundFile("synthetic-connection", "synthetic-account", now.In(wechatJournalZone).Format(wechatDateLayout)))
	if err != nil || len(day.Steps) != 5 {
		t.Fatal("tool attempts disappeared", day, err)
	}
	for index, step := range day.Steps {
		if step.Sequence != uint64(index+1) || step.CallID != entry.CallID || step.Tool != "web_search" || step.DurationMS != 123 || step.ToolHTTPStatus != 503 || step.ToolCode != "http_status" || step.ToolStage != "invocation" {
			t.Fatal("tool metadata was lost", step)
		}
	}
	if day.Steps[3].ToolAttempt != 2 || day.Steps[3].ToolRetries != 1 {
		t.Fatal("retries collapsed", day.Steps)
	}
	entry.ToolCode, entry.ToolStage = "private raw error text", "https://private.invalid/"
	if err := worker.recordRound("synthetic-connection", "synthetic-account", now, entry); err != nil {
		t.Fatal(err)
	}
	raw, err := root.ReadFile(wechatRoundFile("synthetic-connection", "synthetic-account", now.In(wechatJournalZone).Format(wechatDateLayout)))
	if err != nil || strings.Contains(string(raw), "private") {
		t.Fatal("raw diagnostics entered journal", string(raw), err)
	}
}
