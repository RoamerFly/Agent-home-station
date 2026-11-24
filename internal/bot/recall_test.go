package bot

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type recallTestSender struct {
	fakeSender
	page       linuxnative.CatalogPage
	response   linuxnative.ActionStatus
	readback   linuxnative.ActionStatus
	submitErr  error
	submits    int
	inspects   int
	lastAction linuxnative.AdminAction
}

func (*recallTestSender) Account() string { return "test" }
func (s *recallTestSender) Catalog(context.Context, string, string, string, string) (linuxnative.CatalogPage, error) {
	return s.page, nil
}
func (s *recallTestSender) ExecuteAction(_ context.Context, action linuxnative.AdminAction, _ string) (linuxnative.ActionStatus, error) {
	s.submits++
	s.lastAction = action
	return s.response, s.submitErr
}
func (s *recallTestSender) InspectAction(context.Context, string) (linuxnative.ActionStatus, error) {
	s.inspects++
	return s.readback, nil
}

func recallFixture(t *testing.T) (*Worker, *utterancePlan, *recallTestSender, *fakeSource) {
	t.Helper()
	in := companion.Input{Account: "test", Thread: "room@chatroom", TraceID: "request-one"}
	p := &utterancePlan{Input: in, Trace: in.TraceID, Index: 1, Sent: []string{"wrong answer"}}
	d := &delivery{Key: "original-key", ID: "native-job", ServerID: "server-11", State: "accepted",
		Trace: in.TraceID, Thread: in.Thread, Bubble: 0, Text: p.Sent[0], Input: in}
	source := &fakeSource{messages: map[string]linuxnative.Message{"msg_11": {
		ID: "msg_11", AccountID: "test", ThreadID: in.Thread, Direction: "outgoing", ServerID: d.ServerID, Body: d.Text,
	}}}
	sender := &recallTestSender{
		page:     linuxnative.CatalogPage{Items: []map[string]any{{"id": "msg_11", "threadId": in.Thread, "serverId": d.ServerID, "direction": "outgoing"}}},
		response: linuxnative.ActionStatus{ID: "recall-11", AccountID: "test", Kind: "message_recall", State: "queued", ThreadID: in.Thread, MessageID: "msg_11", ServerID: d.ServerID},
		readback: linuxnative.ActionStatus{ID: "recall-11", AccountID: "test", Kind: "message_recall", State: "succeeded", ThreadID: in.Thread, MessageID: "msg_11", ServerID: d.ServerID, RemoteOutcome: "accepted"},
	}
	w := &Worker{config: Config{StateDir: t.TempDir()}, source: source, sender: sender,
		runtime: &runtimeState{Version: 1, Account: "test", Server: "server-1", Plans: []*utterancePlan{p}, Deliveries: []*delivery{d}}}
	return w, p, sender, source
}

func TestRecallAcceptedBubblePersistsOneWriteAndChecksNativeReceipt(t *testing.T) {
	w, p, sender, _ := recallFixture(t)
	state, err := w.recallAcceptedPlanBubble(t.Context(), p, 0)
	if err != nil || state != "pending" || sender.submits != 1 || sender.lastAction.MessageID != "msg_11" || sender.lastAction.ServerID != "server-11" {
		t.Fatalf("first recall: state=%s err=%v submits=%d action=%+v", state, err, sender.submits, sender.lastAction)
	}
	// Restart from the durable queue: inspect the original operation, never
	// submit another recall or infer success from the initial queued receipt.
	raw, err := os.ReadFile(filepath.Join(w.config.StateDir, "messages-v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	var restored runtimeState
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	w.runtime = &restored
	state, err = w.recallAcceptedPlanBubble(t.Context(), restored.Plans[0], 0)
	if err != nil || state != "succeeded" || sender.submits != 1 || sender.inspects != 1 {
		t.Fatalf("readback: state=%s err=%v submits=%d inspects=%d", state, err, sender.submits, sender.inspects)
	}
	state, err = w.recallAcceptedPlanBubble(t.Context(), restored.Plans[0], 0)
	if err != nil || state != "succeeded" || sender.submits != 1 || sender.inspects != 1 {
		t.Fatalf("terminal recall repeated: state=%s err=%v submits=%d", state, err, sender.submits)
	}
}

func TestRecallAcceptedBubbleRejectsWrongNativeIdentity(t *testing.T) {
	w, p, sender, source := recallFixture(t)
	w.config.AuthorizeThread = func(context.Context, string) (bool, error) { return false, nil }
	state, err := w.recallAcceptedPlanBubble(t.Context(), p, 0)
	if state != "unavailable" || err == nil || sender.submits != 0 {
		t.Fatalf("revoked thread reached recall: state=%s err=%v submits=%d", state, err, sender.submits)
	}
	w.config.AuthorizeThread = nil
	message := source.messages["msg_11"]
	message.Direction = "incoming"
	source.messages["msg_11"] = message
	state, err = w.recallAcceptedPlanBubble(t.Context(), p, 0)
	if state != "unavailable" || err == nil || sender.submits != 0 || len(w.runtime.Recalls) != 0 {
		t.Fatalf("foreign message reached recall: state=%s err=%v submits=%d", state, err, sender.submits)
	}
	message.Direction = "outgoing"
	source.messages["msg_11"] = message
	w.runtime.Deliveries[0].ServerID = ""
	state, err = w.recallAcceptedPlanBubble(t.Context(), p, 0)
	if state != "unavailable" || err == nil || sender.submits != 0 {
		t.Fatalf("receipt-free message reached recall: state=%s err=%v submits=%d", state, err, sender.submits)
	}
}

func TestRecallLostResponseStaysUnknownWithoutResubmission(t *testing.T) {
	w, p, sender, _ := recallFixture(t)
	sender.submitErr = errors.New("transport lost response")
	state, err := w.recallAcceptedPlanBubble(t.Context(), p, 0)
	if state != "unknown" || err != nil || sender.submits != 1 {
		t.Fatalf("lost response: state=%s err=%v submits=%d", state, err, sender.submits)
	}
	raw, err := os.ReadFile(filepath.Join(w.config.StateDir, "messages-v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	var restored runtimeState
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	w.runtime = &restored
	state, err = w.recallAcceptedPlanBubble(t.Context(), restored.Plans[0], 0)
	if state != "unknown" || err != nil || sender.submits != 1 || sender.inspects != 0 {
		t.Fatalf("unknown recall repeated: state=%s err=%v submits=%d", state, err, sender.submits)
	}
}

func TestRecallMismatchedReadbackNeverConfirmsSuccess(t *testing.T) {
	w, p, sender, _ := recallFixture(t)
	if state, err := w.recallAcceptedPlanBubble(t.Context(), p, 0); state != "pending" || err != nil {
		t.Fatal(state, err)
	}
	sender.readback.ServerID = "another-message"
	state, err := w.recallAcceptedPlanBubble(t.Context(), p, 0)
	if state != "unknown" || err != nil || sender.submits != 1 || sender.inspects != 1 {
		t.Fatalf("mismatched receipt accepted: state=%s err=%v submits=%d", state, err, sender.submits)
	}
}

func TestRecallNativeGuardProvesNoWrite(t *testing.T) {
	w, p, sender, _ := recallFixture(t)
	sender.submitErr = linuxnative.ErrWriteNotAdmitted
	state, err := w.recallAcceptedPlanBubble(t.Context(), p, 0)
	if state != "unavailable" || !errors.Is(err, linuxnative.ErrWriteNotAdmitted) || len(w.runtime.Recalls) != 0 {
		t.Fatalf("guarded no-write was retained: state=%s err=%v", state, err)
	}
}

func TestRecallSelectsExactSentBubbleEvenWithEqualText(t *testing.T) {
	w, p, sender, source := recallFixture(t)
	p.Index = 2
	p.Sent = append(p.Sent, p.Sent[0])
	second := *w.runtime.Deliveries[0]
	second.Key, second.ID, second.ServerID, second.Bubble = "second-key", "second-job", "server-22", 1
	w.runtime.Deliveries = append(w.runtime.Deliveries, &second)
	source.messages["msg_22"] = linuxnative.Message{ID: "msg_22", AccountID: "test", ThreadID: p.Input.Thread,
		Direction: "outgoing", ServerID: second.ServerID, Body: second.Text}
	sender.page.Items = append(sender.page.Items, map[string]any{"id": "msg_22", "threadId": second.Thread,
		"serverId": second.ServerID, "direction": "outgoing"})
	sender.response.MessageID, sender.response.ServerID = "msg_22", second.ServerID
	state, err := w.recallAcceptedPlanBubble(t.Context(), p, 1)
	if state != "pending" || err != nil || sender.submits != 1 || sender.lastAction.MessageID != "msg_22" || sender.lastAction.ServerID != "server-22" {
		t.Fatalf("wrong equal-text bubble recalled: state=%s err=%v action=%+v", state, err, sender.lastAction)
	}
}
