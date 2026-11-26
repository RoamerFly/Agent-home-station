package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type roundActionNative struct {
	*fakeNative
	messages map[string]linuxnative.Message
}

func (n *roundActionNative) Message(_ context.Context, id string) (linuxnative.Message, error) {
	return n.messages[id], nil
}

func setupRoundAction(t *testing.T) *webFixture {
	t.Helper()
	f := setup(t)
	f.login()
	f.manager.native = &roundActionNative{fakeNative: f.native, messages: map[string]linuxnative.Message{
		"original": {ID: "original", AccountID: "test", ThreadID: "room@chatroom", SenderID: "owner", Direction: "incoming", Type: 1, Body: "请处理这条原消息", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)},
	}}
	return f
}

func TestCancelRoundRetiresPersistenceAndClosesAllTaskAttempts(t *testing.T) {
	f := setupRoundAction(t)
	old := agentTask{ID: "oldtask", Account: "test", Thread: "room@chatroom", Sender: "owner", OriginTrace: "original", State: "needs_review", Title: "旧续办"}
	if err := f.manager.agent.task(old); err != nil {
		t.Fatal(err)
	}
	first, second := "task-oldtask-20261126T155711.000000001", "task-oldtask-20261126T155748.000000002"
	for _, trace := range []string{first, second} {
		f.manager.record(bot.Record{Event: "task_continuation", Category: "decision", ThreadID: old.Thread, MessageID: trace})
	}
	code, body, _ := f.request("POST", "/api/logs/round/cancel", map[string]any{"thread_id": old.Thread, "message_id": first, "task_id": old.ID}, true, true)
	if code != http.StatusOK {
		t.Fatalf("cancel %d %s", code, body)
	}
	stored, ok := f.manager.agent.lookupTask(old.ID, old.Account, old.Thread)
	if !ok || stored.State != "cancelled" {
		t.Fatalf("persistent task still runnable: %+v, %v", stored, ok)
	}
	s := &Server{manager: f.manager}
	for _, trace := range []string{first, second} {
		closed, err := s.roundWasClosed(old.Thread, trace)
		if err != nil || !closed {
			t.Fatalf("attempt still visible: %s, %v, %v", trace, closed, err)
		}
	}
}

func TestRetrySentRoundCreatesNewAttemptWithoutChangingOldReceipt(t *testing.T) {
	f := setupRoundAction(t)
	old := agentTask{ID: "senttask", Account: "test", Thread: "room@chatroom", Sender: "owner", OriginTrace: "original", SourceThread: "room@chatroom", State: "completed", Title: "已发送任务",
		Delivery: &bot.TaskDelivery{State: "accepted", Trace: "old-sent-trace", Accepted: 1, Expected: 1}}
	if err := f.manager.agent.task(old); err != nil {
		t.Fatal(err)
	}
	trace := "task-senttask-20261126T155711.000000001"
	f.manager.record(bot.Record{Event: "message_sent", Category: "send", ThreadID: old.Thread, MessageID: trace, Status: "accepted"})
	code, body, _ := f.request("POST", "/api/logs/round/retry", map[string]any{"thread_id": old.Thread, "message_id": trace, "task_id": old.ID, "mode": "with_memory"}, true, true)
	if code != http.StatusOK {
		t.Fatalf("retry %d %s", code, body)
	}
	var response struct {
		TaskID    string `json:"task_id"`
		AttemptID string `json:"attempt_id"`
	}
	if err := json.Unmarshal(body, &response); err != nil || response.TaskID == "" || response.TaskID == old.ID || response.AttemptID == trace {
		t.Fatalf("retry reused old identity: %+v, %v", response, err)
	}
	original, ok := f.manager.agent.lookupTask(old.ID, old.Account, old.Thread)
	if !ok || original.Delivery == nil || original.Delivery.State != "accepted" {
		t.Fatal("accepted receipt changed")
	}
	newTask, ok := f.manager.agent.lookupTask(response.TaskID, old.Account, old.Thread)
	if !ok || !newTask.RoundRetry || newTask.State != "pending" || newTask.OriginTrace != "original" {
		t.Fatalf("new attempt not runnable: %+v, %v", newTask, ok)
	}
}

func TestFreshRetryFlagsPersistAndChildInheritsIsolation(t *testing.T) {
	f := setupRoundAction(t)
	trace := "original"
	f.manager.record(bot.Record{Event: "message_received", Category: "received", ThreadID: "room@chatroom", MessageID: trace})
	code, body, _ := f.request("POST", "/api/logs/round/retry", map[string]any{"thread_id": "room@chatroom", "message_id": trace, "mode": "fresh"}, true, true)
	if code != http.StatusOK {
		t.Fatalf("fresh retry %d %s", code, body)
	}
	var response struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	// A new agentControl instance reads the durable task file, as it would
	// after a workstation restart.
	reloaded, err := newAgentControl(f.manager.agent.dir)
	if err != nil {
		t.Fatal(err)
	}
	parent, ok := reloaded.lookupTask(response.TaskID, "test", "room@chatroom")
	if !ok || !parent.RoundRetryFresh || parent.Title != "处理消息" {
		t.Fatalf("fresh attempt lost across restart: %+v, %v", parent, ok)
	}
	child := agentTask{ID: "new-child", ParentID: parent.ID, Account: parent.Account, Thread: parent.Thread, Sender: parent.Sender,
		OriginTrace: parent.OriginTrace, State: "pending", Title: "生成结果"}
	child, err = reloaded.saveTask(child)
	if err != nil {
		t.Fatal(err)
	}
	in := taskResultInput(child, &companion.Input{TaskID: child.ID, OriginTraceID: child.OriginTrace})
	if !in.FreshRoundLineage || !in.ExplicitRoundRetry || in.FreshRoundPriorTraceID != trace {
		t.Fatalf("child lost fresh isolation: %+v", in)
	}
	if strings.Contains(parent.RequestedText, "重试") {
		t.Fatal("fresh request exposed retry wording")
	}
}
