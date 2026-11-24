package bot

import (
	"context"
	"testing"
	"time"

	"wechat-bot/internal/companion"
)

func TestManualCompletionStopsWaitingDeliveryWithoutSending(t *testing.T) {
	w := newTestWorker(t, &fakeSource{}, &fakeModel{}, &fakeSender{})
	w.runtime = &runtimeState{Deliveries: []*delivery{{Trace: "trace-1", Thread: "filehelper", State: "waiting", Next: time.Now().Add(time.Hour)}}, Plans: []*utterancePlan{{Input: companion.Input{TraceID: "trace-1", Thread: "filehelper"}, State: "review_pending"}}, Inbox: []inboxItem{{Event: fixtureEvent("1", "trace-1", "filehelper", "incoming", "remote"), Phase: "processing"}}}
	w.manualCompletions = make(chan manualCompletionRequest, 1)
	go func() { _ = w.drainManualCompletions(context.Background()) }()
	if err := w.ManuallyComplete(context.Background(), "trace-1"); err != nil {
		t.Fatal(err)
	}
	if got := w.runtime.Deliveries[0].State; got != "stopped" {
		t.Fatalf("delivery state = %q", got)
	}
	if got := w.runtime.Plans[0].State; got != "stopped" {
		t.Fatalf("plan state = %q", got)
	}
	if got := w.runtime.Inbox[0].Phase; got != "completed" {
		t.Fatalf("inbox phase = %q", got)
	}
}

func TestManualCompletionPersistsClosedRoundAndStopsOnlyMatchingThread(t *testing.T) {
	w := newTestWorker(t, &fakeSource{}, &fakeModel{}, &fakeSender{})
	w.runtime = &runtimeState{
		Version: 1, Server: "server-1", Account: "test",
		Inbox: []inboxItem{
			{Event: fixtureEvent("1", "trace-1", "filehelper", "incoming", "remote"), Phase: "processing"},
			{Event: fixtureEvent("2", "child", "filehelper", "incoming", "remote"), Phase: "backlog_grouped", BacklogParent: "trace-1"},
			{Event: fixtureEvent("3", "trace-1", "room@chatroom", "incoming", "remote"), Phase: "received"},
		},
		Deliveries: []*delivery{
			{Trace: "trace-1", Thread: "filehelper", State: "waiting", Next: time.Now()},
			{Trace: "continuation", Thread: "filehelper", Input: companion.Input{Thread: "filehelper", OriginTraceID: "trace-1"}, State: "waiting", Next: time.Now()},
			{Trace: "owner-update", Thread: "owner", Input: companion.Input{Thread: "owner", OriginThread: "filehelper", OriginTraceID: "trace-1"}, State: "waiting", Next: time.Now()},
			{Trace: "other-owner-update", Thread: "owner", Input: companion.Input{Thread: "owner", OriginThread: "room@chatroom", OriginTraceID: "trace-1"}, State: "waiting", Next: time.Now()},
		},
	}
	if err := w.completeTrace("filehelper", "trace-1"); err != nil {
		t.Fatal(err)
	}
	if !w.roundClosed("filehelper", "trace-1") || w.roundClosed("room@chatroom", "trace-1") || w.runtime.Inbox[0].Phase != "completed" || w.runtime.Inbox[1].Phase != "completed" || w.runtime.Inbox[2].Phase != "received" || w.runtime.Deliveries[0].State != "stopped" || w.runtime.Deliveries[1].State != "stopped" || w.runtime.Deliveries[2].State != "stopped" || w.runtime.Deliveries[3].State != "waiting" {
		t.Fatal("close did not fence matching round only")
	}
	w2 := newTestWorker(t, &fakeSource{}, &fakeModel{}, &fakeSender{})
	w2.config.StateDir = w.config.StateDir
	if err := w2.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	if !w2.roundClosed("filehelper", "trace-1") || w2.runtime.Deliveries[0].State != "stopped" || w2.runtime.Deliveries[1].State != "stopped" || w2.runtime.Deliveries[2].State != "stopped" || w2.runtime.Deliveries[3].State != "waiting" {
		t.Fatal("closed marker was not durable")
	}
}
