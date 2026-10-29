package bot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func TestQueuePauseResumeCancelPreservesIdentityAndIsolation(t *testing.T) {
	w, n, _, _ := mediaWorker(t)
	old := companion.Input{Account: "test", Thread: "room@chatroom", Sender: "owner", TraceID: "old"}
	d := &mediaDelivery{Key: "stable", Trace: "old", Input: old, ID: "job-old", Kind: "voice", State: "waiting"}
	n.status = linuxnative.ToolStatus{ID: d.ID, ThreadID: old.Thread, State: "running"}
	surf := &mediaDelivery{Key: "surf", Trace: "surf", Input: companion.Input{Thread: old.Thread, Surf: true}, Kind: "video", State: "prepared"}
	foreign := &mediaDelivery{Key: "foreign", Trace: "foreign", Input: companion.Input{Thread: old.Thread, Sender: "another"}, State: "prepared"}
	accepted := &mediaDelivery{Key: "accepted", Trace: "old", Input: old, State: "accepted"}
	w.runtime.Media = []*mediaDelivery{d, surf, foreign, accepted}
	p := &utterancePlan{Trace: "old", Input: old, State: "queued", MediaCaption: true, Messages: []string{"caption"}}
	w.runtime.Plans = append(w.runtime.Plans, p)
	in := old
	in.TraceID = "new"
	call := func(action string) {
		t.Helper()
		if _, e := w.controlQueue(context.Background(), in, false, json.RawMessage(`{"action":"`+action+`"}`)); e != nil {
			t.Fatal(e)
		}
	}
	call("pause")
	if !d.Paused || !surf.Paused || foreign.Paused || !p.Paused || accepted.State != "accepted" || n.cancels != 1 {
		t.Fatal("pause failed", d, surf, foreign, p, n.cancels)
	}
	if e := w.loadRuntime("server-1"); e != nil {
		t.Fatal(e)
	}
	d = w.runtime.Media[0]
	if !d.Paused || d.ID != "job-old" {
		t.Fatal("lost durable pause")
	}
	d.Next = time.Time{}
	w.reconcileMedia(context.Background())
	if n.sends != 0 || n.retries != 0 {
		t.Fatal("paused work advanced")
	}
	call("resume")
	if d.Paused || d.ID != "job-old" || n.sends != 0 {
		t.Fatal("resume cloned queue")
	}
	call("cancel")
	if !d.UserCancelled || foreign.UserCancelled {
		t.Fatal("cancellation scope")
	}
	call("resume")
	if !d.UserCancelled {
		t.Fatal("cancelled task resurrected")
	}
	if _, e := w.controlQueue(context.Background(), in, false, json.RawMessage(`{"action":"pause|cancel"}`)); e == nil {
		t.Fatal("invalid action")
	}
}

func TestQueueResumeReportsNativeBoundaryWithoutCloning(t *testing.T) {
	w, n, _, _ := mediaWorker(t)
	in := companion.Input{Account: "test", Thread: "room@chatroom", Sender: "owner", TraceID: "new"}
	d := &mediaDelivery{Key: "same-key", Trace: "old", ID: "same-job", Input: companion.Input{Thread: in.Thread, Sender: in.Sender}, State: "waiting", Paused: true}
	w.runtime.Media = []*mediaDelivery{d}
	n.status = linuxnative.ToolStatus{ID: d.ID, ThreadID: in.Thread, State: "cancelled", Retryable: false}
	result, err := w.controlQueue(context.Background(), in, true, json.RawMessage(`{"action":"resume"}`))
	if err != nil || !d.Paused || d.ID != "same-job" || n.sends != 0 || n.retries != 0 || !strings.Contains(result, "native_cancelled_job_not_retryable") {
		t.Fatal("hid native resume boundary or duplicated queue", result, err, d)
	}
}

func TestAddressedInterruptionPausesOwnAndSurfMedia(t *testing.T) {
	w, _, _, _ := mediaWorker(t)
	old := companion.Input{Thread: "filehelper", Sender: "requester", TraceID: "old"}
	w.runtime.Media = []*mediaDelivery{{Key: "one", Trace: "old", Input: old, State: "prepared"}}
	w.pauseInterruptedMedia(linuxnative.Message{ID: "new", ThreadID: "filehelper", SenderID: "requester", Type: 1, Body: "换成文件，不发语音了"})
	if !w.runtime.Media[0].Paused {
		t.Fatal("interruption did not pause pending upload")
	}
}
