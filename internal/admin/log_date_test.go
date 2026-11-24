package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wechat-bot/internal/linuxnative"
)

type logSendNative struct {
	*fakeTools
	statuses map[string]linuxnative.ToolStatus
}

func (n *logSendNative) ToolSendStatus(_ context.Context, id string) (linuxnative.ToolStatus, error) {
	if status, ok := n.statuses[id]; ok {
		return status, nil
	}
	return linuxnative.ToolStatus{}, errors.New("unknown synthetic send")
}

func TestLogSendStatesUsesExactAcceptedNativeReceipt(t *testing.T) {
	f := setup(t)
	f.manager.native = &logSendNative{fakeTools: &fakeTools{fakeNative: f.native}, statuses: map[string]linuxnative.ToolStatus{
		"accepted":     {ID: "accepted", ThreadID: "group@chatroom", State: "accepted", ServerID: "server-1"},
		"wrong-thread": {ID: "wrong-thread", ThreadID: "elsewhere", State: "accepted", ServerID: "server-2"},
		"no-receipt":   {ID: "no-receipt", ThreadID: "group@chatroom", State: "accepted"},
	}}
	items := []LogEntry{
		{Event: "agent_cross_send", ThreadID: "group@chatroom", SendID: "accepted"},
		{Event: "cross_conversation_reply_queued", ThreadID: "group@chatroom", SendID: "wrong-thread"},
		{Event: "agent_cross_send", ThreadID: "group@chatroom", SendID: "no-receipt"},
		{Event: "message_progress", ThreadID: "group@chatroom", SendID: "progress"},
	}
	got := (&Server{manager: f.manager}).logSendStates(t.Context(), items)
	if len(got) != 1 || !got["group@chatroom\x00accepted"] {
		t.Fatalf("native receipt projection = %+v", got)
	}
}

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

func TestLogTaskStatesLatestOutcomeWinsSharedOrigin(t *testing.T) {
	f := setup(t)
	thread, origin := "group@chatroom", "song-request"
	now := time.Now().UTC()
	old := agentTask{ID: "old-pending", Account: "test", Thread: thread, OriginTrace: origin, State: "pending", Updated: now.Add(-time.Minute)}
	finished := agentTask{ID: "new-completed", Account: "test", Thread: thread, OriginTrace: origin, State: "completed", Updated: now}
	f.manager.agent.mu.Lock()
	f.manager.agent.data.Tasks[old.ID] = old
	f.manager.agent.data.Tasks[finished.ID] = finished
	f.manager.agent.mu.Unlock()
	states := (&Server{manager: f.manager}).logTaskStates([]LogEntry{{Event: "message_received", ThreadID: thread, MessageID: origin}})
	if got := states[thread+"\x00"+origin]; got.State != "completed" || got.Active {
		t.Fatalf("older duplicate pending task revived completed round: %+v", got)
	}
}

func TestLogTaskStatesReadsArchivedCancellation(t *testing.T) {
	f := setup(t)
	thread, origin := "owner-private", "older-request"
	ended := agentTask{ID: "archived-task", Account: "test", Thread: thread, OriginTrace: origin, State: "cancelled", Updated: time.Now().UTC()}
	f.manager.agent.mu.Lock()
	f.manager.agent.archived[ended.ID] = ended
	f.manager.agent.mu.Unlock()
	states := (&Server{manager: f.manager}).logTaskStates([]LogEntry{{Event: "task_execution_waiting", ThreadID: thread, MessageID: origin}})
	if got := states[thread+"\x00"+origin]; got.State != "cancelled" || got.Active {
		t.Fatalf("archived cancellation still shown as waiting: %+v", got)
	}
}

func TestLogTaskStatesLatestReminderWinsSharedOrigin(t *testing.T) {
	f := setup(t)
	thread, origin := "group@chatroom", "reminder-request"
	now := time.Now().UTC()
	old := agentTask{ID: "old-reminder", Account: "test", Thread: thread, OriginTrace: origin, State: "pending", Updated: now.Add(-time.Hour), Reminder: &reminderSpec{FollowUpTaskID: "old-follow"}}
	oldFollow := agentTask{ID: "old-follow", Account: "test", Thread: thread, OriginTrace: origin, State: "pending", Updated: now.Add(-time.Minute)}
	latest := agentTask{ID: "new-reminder", Account: "test", Thread: thread, OriginTrace: origin, State: "completed", Updated: now, Reminder: &reminderSpec{FollowUpTaskID: "new-follow"}}
	newFollow := agentTask{ID: "new-follow", Account: "test", Thread: thread, OriginTrace: origin, State: "completed", Updated: now.Add(time.Second)}
	f.manager.agent.mu.Lock()
	for _, task := range []agentTask{old, oldFollow, latest, newFollow} {
		f.manager.agent.data.Tasks[task.ID] = task
	}
	f.manager.agent.mu.Unlock()
	states := (&Server{manager: f.manager}).logTaskStates([]LogEntry{{Event: "reminder_due", ThreadID: thread, MessageID: origin}})
	if got := states[thread+"\x00"+origin]; got.State != "completed" || got.Active {
		t.Fatalf("older reminder revived completed follow-up: %+v", got)
	}
}

func TestUnfinishedGroupTaskKeepsOriginalRoundActive(t *testing.T) {
	f := setup(t)
	updated := time.Now().UTC()
	task := agentTask{ID: "image-task", Account: "test", Thread: "group@chatroom", RequestThread: "group@chatroom", OriginTrace: "image-request", State: "pending", Updated: updated}
	f.manager.agent.mu.Lock()
	f.manager.agent.data.Tasks[task.ID] = task
	f.manager.agent.mu.Unlock()
	states := (&Server{manager: f.manager}).logTaskStates([]LogEntry{{Event: "message_sent", ThreadID: task.Thread, MessageID: task.OriginTrace}})
	got := states[task.Thread+"\x00"+task.OriginTrace]
	if got.State != "pending" || !got.Active {
		t.Fatalf("unfinished image task lost after a text reply: %+v", got)
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
