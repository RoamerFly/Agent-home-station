package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

func TestJournalRetentionArchivesBeforeRemovalAndSurvivesRestart(t *testing.T) {
	f := setup(t)
	old := LogEntry{ID: "synthetic-old", Time: time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339Nano), Content: "过去的合成正文", ThreadID: "synthetic@chatroom"}
	f.manager.journal = []LogEntry{old}
	f.manager.record(bot.Record{Event: "message_received", ThreadID: "filehelper", Content: "新消息"})
	if len(f.manager.journal) != 1 {
		t.Fatal("old journal not rotated")
	}
	items, _, err := f.manager.historyArchive().List("activity", activityScope(), "", 20)
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	copy, err := NewManager(f.dir, "", f.native)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	recovered, err := copy.archiveLogs(items[0].ID)
	if err != nil || len(recovered) != 1 || recovered[0].Content != old.Content {
		t.Fatal(recovered, err)
	}
}

func TestJournalCountRotationArchivesBlocksAndKeepsEveryRow(t *testing.T) {
	f := setup(t)
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	for i := 0; i < 1000; i++ {
		f.manager.journal = append(f.manager.journal, LogEntry{ID: fmt.Sprintf("synthetic-%d", i), Time: stamp, Content: "synthetic"})
	}
	f.manager.record(bot.Record{Event: "message_received", Content: "latest"})
	if len(f.manager.journal) != 873 {
		t.Fatal("count rotation should archive a block", len(f.manager.journal))
	}
	items, _, err := f.manager.historyArchive().List("activity", activityScope(), "", 20)
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	archived, err := f.manager.archiveLogs(items[0].ID)
	if err != nil || len(archived) != 128 || archived[0].ID != "synthetic-0" || archived[127].ID != "synthetic-127" {
		t.Fatal("archive range lost", err, len(archived))
	}
	if f.manager.journal[0].ID != "synthetic-128" {
		t.Fatal("active continuation lost")
	}
}
func TestArchiveFailurePreservesActiveJournalAndReportsCapacityError(t *testing.T) {
	f := setup(t)
	if err := os.WriteFile(filepath.Join(f.dir, "archive"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	f.manager.journal = []LogEntry{{ID: "old", Time: time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339Nano), Content: "must remain"}}
	f.manager.record(bot.Record{Event: "message_received", Content: "new"})
	if len(f.manager.journal) != 2 || f.manager.storageError == "" {
		t.Fatal("archive failure hid data")
	}
	var saved []LogEntry
	b, _ := os.ReadFile(filepath.Join(f.dir, "activity.json"))
	if json.Unmarshal(b, &saved) != nil || len(saved) != 2 {
		t.Fatal("failure was not recoverable")
	}
	state, err := f.manager.storageStatus(context.Background())
	if err != nil || state["error"] == "" {
		t.Fatal(state, err)
	}
}
func TestArchiveEndpointsAuthenticateAndScopeMemoryWithoutActionReplay(t *testing.T) {
	f := setup(t)
	if code, _, _ := f.request("GET", "/api/storage", nil, false, false); code != 401 {
		t.Fatal(code)
	}
	f.login()
	c := f.manager.Config().Companion
	c.Permissions.Remember = true
	c.MemoryMessages = 1
	now := time.Now()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "first", Text: "archived synthetic text"}
	if err := f.manager.memory.Observe(in, c, now); err != nil {
		t.Fatal(err)
	}
	in.TraceID = "second"
	in.Text = "active"
	f.manager.memory.Observe(in, c, now.Add(time.Second))
	id := companion.ScopeID("wechat", "test", "filehelper")
	route := "/api/archives/memory?scope=" + id
	code, b, _ := f.request("GET", route, nil, false, false)
	if code != 200 {
		t.Fatal(code, string(b))
	}
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	json.Unmarshal(b, &page)
	if len(page.Items) != 1 {
		t.Fatal(string(b))
	}
	code, b, response := f.request("GET", "/api/archives/memory/"+page.Items[0].ID+"?scope="+id+"&download=1", nil, false, false)
	if code != 200 || response.Header.Get("Content-Disposition") == "" || !bytes.Contains(b, []byte("archived synthetic text")) || !bytes.Contains(b, []byte(`"replays_actions":false`)) {
		t.Fatal(code, string(b))
	}
	other := companion.ScopeID("wechat", "other-account", "filehelper")
	if code, _, _ := f.request("GET", "/api/archives/memory?scope="+other, nil, false, false); code != 404 {
		t.Fatal("cross account read", code)
	}
	if len(f.manager.tools.data.Sends) != 0 {
		t.Fatal("archive read created a send")
	}
}
