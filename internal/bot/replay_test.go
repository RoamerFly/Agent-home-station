package bot

import (
	"context"
	"reflect"
	"testing"
	"time"

	"wechat-bot/internal/linuxnative"
)

func TestTriggerRoundProcessingDoesNotAdvanceOldBatchChild(t *testing.T) {
	const thread = "room@chatroom"
	at := time.Now().Add(-time.Hour).UTC()
	w := durableWorker(t, &reliableFake{})
	w.source = &fakeSource{messages: map[string]linuxnative.Message{
		"anchor": {ID: "anchor", ThreadID: thread, Direction: "incoming", CreatedAt: at.Format(time.RFC3339Nano)},
	}}
	w.allowed[thread] = true
	// Hold the queue so the test can advance the retried anchor explicitly.
	w.config.Draining = func() bool { return true }
	priorChild := inboxItem{Event: fixtureEvent("1", "image", thread, "incoming", "remote"), Phase: "stopped", BacklogParent: "anchor", Updated: at}
	w.runtime.Inbox = []inboxItem{
		priorChild,
		{Event: fixtureEvent("2", "anchor", thread, "incoming", "remote"), Phase: "stopped", BacklogParent: "anchor", Updated: at},
	}

	result, err := w.triggerRoundProcessing(context.Background(), thread, "anchor")
	if err != nil || result.Status != "queued" {
		t.Fatalf("expected anchor to be queued, got %+v, %v", result, err)
	}
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	w.queuePhase("anchor", "replied")
	if w.persistErr != nil || w.runtime.Inbox[1].Phase != "replied" {
		t.Fatalf("expected anchor to be replied, got %s, %v", w.runtime.Inbox[1].Phase, w.persistErr)
	}
	priorChild.BacklogParent = ""
	if got := w.runtime.Inbox[0]; !reflect.DeepEqual(got, priorChild) {
		t.Fatalf("unselected child changed during anchor retry: got %+v, want %+v", got, priorChild)
	}
}
