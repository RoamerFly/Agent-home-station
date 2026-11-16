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
