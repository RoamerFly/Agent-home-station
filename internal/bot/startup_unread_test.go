package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type pagedUnreadSource struct{ fakeSource }

func TestOwnerSingleMessageRetryIsNotConvertedToUnreadBatch(t *testing.T) {
	w, _, _ := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) {
		t.Fatal("capture must not call a model")
		return "", nil
	}))
	w.source = &fakeSource{}
	w.runtime.Inbox = []inboxItem{
		{OwnerModelReview: true, Backlog: true, Phase: "received", Event: fixtureEvent("1", "explicit", "room@chatroom", "incoming", "remote")},
		{Phase: "received", Event: fixtureEvent("2", "ordinary", "room@chatroom", "incoming", "remote")},
	}
	state := checkpoint{Version: 1, ServerID: "server-1", Account: "test", Cursor: "2"}
	if err := w.captureStartupUnread(t.Context(), filepath.Join(w.config.StateDir, "checkpoint.json"), &state); err != nil {
		t.Fatal(err)
	}
	if w.runtime.Inbox[0].Backlog || !w.runtime.Inbox[1].Backlog || !w.runtime.Inbox[0].OwnerModelReview {
		t.Fatal("explicit retry lost its independent trigger or changed ordinary recovery", w.runtime.Inbox)
	}
}

func (s *pagedUnreadSource) Events(ctx context.Context, after string) (linuxnative.EventPage, error) {
	n, _ := strconv.Atoi(after)
	page := linuxnative.EventPage{NextCursor: after}
	for _, e := range s.events {
		v, _ := strconv.Atoi(e.Cursor)
		if v > n {
			page.Items = append(page.Items, e)
			page.NextCursor = e.Cursor
			if len(page.Items) == 100 {
				break
			}
		}
	}
	return page, nil
}

func TestStartupUnreadGroupsAcrossPagesSpeakersAndPreservesTimes(t *testing.T) {
	var calls atomic.Int32
	var current struct {
		Unread []companion.ConversationEvent `json:"unread_messages"`
		Now    string                        `json:"current_time"`
		Zone   string                        `json:"timezone"`
		Sender string                        `json:"request_actor_id"`
	}
	w, c, _ := backgroundWorker(t, generationModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
		calls.Add(1)
		if err := json.Unmarshal([]byte(turns[len(turns)-1]["content"]), &current); err != nil {
			t.Error(err)
		}
		return `{"action":"silent"}`, nil
	}))
	c.Config.Permissions.ObserveGroups = true
	c.Config.Permissions.AutonomousGroups = true
	c.Config.MemoryMessages = 500
	w.config.GroupRules = map[string]GroupRule{"room@chatroom": {Mode: "social"}}
	source := &pagedUnreadSource{fakeSource: fakeSource{messages: map[string]linuxnative.Message{}}}
	base := time.Now().Add(-4 * time.Hour).Truncate(time.Second)
	for i := 1; i <= 104; i++ {
		id := fmt.Sprintf("offline-%d", i)
		m := fixtureMessage(id, "room@chatroom", fmt.Sprintf("第%d条原话", i), "incoming")
		m.SenderID = fmt.Sprintf("wxid_person_%d", i%2)
		m.CreatedAt = base.Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		source.messages[id] = m
		source.events = append(source.events, fixtureEvent(strconv.Itoa(i), id, m.ThreadID, "incoming", "remote"))
	}
	w.source = source
	w.enableGenerations()
	defer w.closeGenerations()
	state := checkpoint{Version: 1, ServerID: "server-1", Account: "test", Cursor: "0"}
	if err := w.captureStartupUnread(context.Background(), filepath.Join(w.config.StateDir, "checkpoint.json"), &state); err != nil {
		t.Fatal(err)
	}
	if state.Cursor != "104" || len(w.runtime.Inbox) != 104 {
		t.Fatal("did not collect full unread tail")
	}
	if err := w.drainInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	pumpGeneration(t, w, func() bool { return w.generation.occupied() == 0 })
	if calls.Load() != 1 || len(current.Unread) != 104 || current.Now == "" || current.Zone == "" || current.Sender != "wxid_person_0" {
		t.Fatal("not one timestamped conversation turn", calls.Load(), len(current.Unread), current.Sender)
	}
	for _, index := range []int{0, 103} {
		actual, err := time.Parse(time.RFC3339, current.Unread[index].At)
		expected, expectedErr := time.Parse(time.RFC3339, source.messages[current.Unread[index].MessageID].CreatedAt)
		if err != nil || expectedErr != nil || !actual.Equal(expected) {
			t.Fatal("original time lost", current.Unread[index].At, expected)
		}
	}
	scope := c.Store.Context(companion.Input{Channel: "wechat", Account: "test", Thread: "room@chatroom"}, c.Config, time.Now())
	if len(scope.Messages) != 104 {
		t.Fatal("original messages duplicated/replaced with synthetic summary", len(scope.Messages))
	}
	for _, item := range w.runtime.Inbox {
		if item.Phase != "silent" {
			t.Fatal("child message not settled", item.Phase)
		}
	}
	// Confirmed decisions do not become unread again on the next launch.
	if err := w.captureStartupUnread(context.Background(), filepath.Join(w.config.StateDir, "checkpoint.json"), &state); err != nil {
		t.Fatal(err)
	}
	w.drainInbox(context.Background())
	if calls.Load() != 1 {
		t.Fatal("already viewed messages replayed")
	}
}
