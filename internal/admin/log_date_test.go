package admin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReminderRoundUsesFollowUpTaskOutcome(t *testing.T) {
	f := setup(t)
	updated := time.Now().UTC()
	parent := agentTask{ID: "reminder", Account: "test", Thread: "group@chatroom", OriginTrace: "request-message", State: "completed", Updated: updated, Reminder: &reminderSpec{FollowUpTaskID: "follow-up"}}
	child := agentTask{ID: "follow-up", Account: "test", Thread: parent.Thread, ParentID: parent.ID, State: "completed", Updated: updated}
	f.manager.agent.mu.Lock()
	f.manager.agent.data.Tasks[parent.ID] = parent
	f.manager.agent.data.Tasks[child.ID] = child
	f.manager.agent.mu.Unlock()
	states := (&Server{manager: f.manager}).logTaskStates([]LogEntry{{Event: "reminder_due", ThreadID: parent.Thread, MessageID: parent.OriginTrace}})
	if got := states[parent.Thread+"\x00"+parent.OriginTrace]; got.State != "completed" {
		t.Fatalf("completed reminder follow-up still pending: %+v", got)
	}
}

func TestLogInboxStatesReadsOnlyMessagePhases(t *testing.T) {
	f := setup(t)
	f.manager.stateDir = t.TempDir()
	server := &Server{manager: f.manager}
	states, loaded := server.logInboxStates()
	if loaded || states != nil {
		t.Fatal("missing checkpoint must not be treated as an empty inbox")
	}
	data := []byte(`{"inbox":[{"phase":"received","event":{"data":{"messageId":"message-one","content":"private text"}}}]}`)
	if err := os.WriteFile(filepath.Join(f.manager.stateDir, "messages-v2.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	states, loaded = server.logInboxStates()
	if !loaded || states["message-one"].Phase != "received" || len(states) != 1 {
		t.Fatal("inbox phase projection failed")
	}
}

func TestLogDateIncludesArchiveAndShanghaiBoundaries(t *testing.T) {
	f := setup(t)
	f.login()
	f.manager.journal = []LogEntry{
		{ID: "today", Time: "2026-10-29T16:00:00Z", Event: "message_received"},
		{ID: "tomorrow", Time: "2026-10-30T16:00:00Z", Event: "message_received"},
		{ID: "yesterday", Time: "2026-10-29T15:59:59Z", Event: "message_received"},
	}
	for i := 0; i < 12; i++ {
		_, err := f.manager.historyArchive().Put("activity", activityScope(), []LogEntry{{ID: fmt.Sprintf("archive-%d", i), Time: "2026-10-30T08:00:00Z", Event: "message_received"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	after := ""
	pages := 0
	for {
		code, data, _ := f.request("GET", "/api/logs?date=2026-10-30&after="+after, nil, false, false)
		if code != 200 {
			t.Fatal(code, string(data))
		}
		var page struct {
			Items    []LogEntry `json:"items"`
			Next     string     `json:"next"`
			Complete bool       `json:"complete"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			t.Fatal(err)
		}
		for _, entry := range page.Items {
			if seen[entry.ID] {
				t.Fatal("repeated page", entry.ID)
			}
			seen[entry.ID] = true
			if entry.Category != "received" {
				t.Fatal("missing category")
			}
		}
		pages++
		if page.Complete {
			break
		}
		if pages > 3 || page.Next == "" {
			t.Fatal("bad continuation")
		}
		after = page.Next
	}
	if pages != 2 || len(seen) != 13 || !seen["today"] || seen["yesterday"] || seen["tomorrow"] {
		t.Fatal("date coverage", pages, seen)
	}
	for _, query := range []string{"date=2026-02-30", "date=", "date=2026-10-30&after=../other"} {
		if code, _, _ := f.request("GET", "/api/logs?"+query, nil, false, false); code != 400 {
			t.Fatal(query, code)
		}
	}
}
