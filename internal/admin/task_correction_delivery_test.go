package admin

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
)

func correctedVideoFixture(t *testing.T, f *webFixture, id string) agentTask {
	t.Helper()
	correction := time.Now().UTC().Add(-time.Minute)
	task := agentTask{
		ID: id, Account: "test", Thread: "room@chatroom", Sender: "owner", State: "running",
		CorrectionAt: correction, CorrectionRequiredMediaKind: "video", CorrectionReferenceImageID: "new-image",
		CorrectionTraces: []string{"new-trace"}, CorrectionWorkStartedAt: correction.Add(time.Second),
		CorrectionMediaPreparedAt: correction.Add(2 * time.Second), CorrectionPreparedMediaID: "new-media",
	}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	return task
}

func TestCorrectedVideoCompletionWaitsForNewMediaReceipt(t *testing.T) {
	f := setup(t)
	task := correctedVideoFixture(t, f, "corrected-video-delivery")
	task.State = "completed"
	saved, err := f.manager.agent.saveTask(task)
	if err != nil || saved.State != "waiting" || saved.WorkCompleted || !saved.CorrectionCompletionRequested || !saved.Next.After(time.Now()) || saved.Delivery != nil {
		t.Fatalf("prepared video without send plan should retry: %+v %v", saved, err)
	}
	in := companion.Input{Account: task.Account, Thread: task.Thread, Sender: task.Sender, TaskID: task.ID, TraceID: "new-trace"}
	if err := f.manager.updateAgentDelivery(in, bot.TaskDelivery{Trace: "new-trace", State: "waiting_delivery", Expected: 1, VideoExpected: 1}); err != nil {
		t.Fatal(err)
	}
	waiting, ok := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if !ok || waiting.State != "waiting_delivery" || !waiting.WorkCompleted || !waiting.Next.IsZero() {
		t.Fatalf("new video send plan did not take over receipt tracking: %+v", waiting)
	}
	if err := f.manager.updateAgentDelivery(in, bot.TaskDelivery{Trace: "new-trace", State: "accepted", Expected: 1, Accepted: 1, VideoExpected: 1, VideoAccepted: 1, VideoSourceID: "new-media", TextExpected: 1, TextAccepted: 1}); err != nil {
		t.Fatal(err)
	}
	completed, ok := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if !ok || completed.State != "completed" || !completed.WorkCompleted || len(f.manager.agent.active(task.Account, task.Thread)) != 0 {
		t.Fatalf("accepted video receipt did not finish task: %+v", completed)
	}
}

func TestCorrectedVideoTextOnlyReceiptWithoutCompletionIntentRetries(t *testing.T) {
	f := setup(t)
	task := correctedVideoFixture(t, f, "corrected-video-text-only")
	in := companion.Input{Account: task.Account, Thread: task.Thread, Sender: task.Sender, TaskID: task.ID, TraceID: "new-trace"}
	if err := f.manager.updateAgentDelivery(in, bot.TaskDelivery{Trace: "new-trace", State: "accepted", TextExpected: 1, TextAccepted: 1}); err != nil {
		t.Fatal(err)
	}
	waiting, ok := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if !ok || waiting.State != "waiting" || waiting.WorkCompleted || !waiting.Next.After(time.Now()) {
		t.Fatalf("text-only receipt should recheck missing video: %+v", waiting)
	}
}

func TestCorrectedVideoImageReceiptCannotFinishVideo(t *testing.T) {
	f := setup(t)
	task := correctedVideoFixture(t, f, "corrected-video-image-only")
	task.State = "completed"
	if _, err := f.manager.agent.saveTask(task); err != nil {
		t.Fatal(err)
	}
	in := companion.Input{Account: task.Account, Thread: task.Thread, Sender: task.Sender, TaskID: task.ID, TraceID: "new-trace"}
	if err := f.manager.updateAgentDelivery(in, bot.TaskDelivery{Trace: "new-trace", State: "accepted", Expected: 1, Accepted: 1}); err != nil {
		t.Fatal(err)
	}
	waiting, ok := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if !ok || waiting.State != "waiting" || waiting.WorkCompleted || !waiting.Next.After(time.Now()) {
		t.Fatalf("accepted image counted as corrected video: %+v", waiting)
	}
}

func TestCorrectedVideoRejectsDifferentAcceptedVideoSource(t *testing.T) {
	f := setup(t)
	task := correctedVideoFixture(t, f, "corrected-video-wrong-source")
	task.State = "completed"
	if _, err := f.manager.agent.saveTask(task); err != nil {
		t.Fatal(err)
	}
	in := companion.Input{Account: task.Account, Thread: task.Thread, Sender: task.Sender, TaskID: task.ID, TraceID: "new-trace"}
	if err := f.manager.updateAgentDelivery(in, bot.TaskDelivery{Trace: "new-trace", State: "accepted", Expected: 1, Accepted: 1, VideoExpected: 1, VideoAccepted: 1, VideoSourceID: "old-video", TextExpected: 1, TextAccepted: 1}); err != nil {
		t.Fatal(err)
	}
	waiting, ok := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if !ok || waiting.State != "waiting" || waiting.WorkCompleted || !waiting.Next.After(time.Now()) || waiting.correctedDeliveryComplete() {
		t.Fatalf("different video accepted as corrected output: %+v", waiting)
	}
}

func TestCorrectedVideoRequiresTextReceiptUnlessCaptionOmitted(t *testing.T) {
	f := setup(t)
	task := correctedVideoFixture(t, f, "corrected-video-caption")
	task.Delivery = &bot.TaskDelivery{Trace: "new-trace", State: "accepted", Expected: 1, Accepted: 1,
		VideoExpected: 1, VideoAccepted: 1, VideoSourceID: "new-media", TextExpected: 1}
	if task.correctedDeliveryComplete() {
		t.Fatal("missing required caption was accepted")
	}
	task.Delivery.CaptionOmitted = true
	if !task.correctedDeliveryComplete() {
		t.Fatal("explicitly omitted caption blocked accepted video")
	}
}

func TestAdministratorCompleteCorrectedVideoReportsVerifiedReceipt(t *testing.T) {
	f := setup(t)
	f.login()
	task := correctedVideoFixture(t, f, "corrected-video-admin-complete")
	task.State = "waiting_delivery"
	task.WorkCompleted = true
	task.Delivery = &bot.TaskDelivery{Trace: "new-trace", State: "accepted", Expected: 1, Accepted: 1, VideoExpected: 1, VideoAccepted: 1, VideoSourceID: "new-media", TextExpected: 1, TextAccepted: 1}
	if err := f.manager.agent.task(task); err != nil {
		t.Fatal(err)
	}
	status, _, _ := f.request(http.MethodPost, "/api/agent/tasks/"+task.ID, map[string]string{"action": "complete"}, true, true)
	if status != http.StatusOK {
		t.Fatalf("admin completion status: %d", status)
	}
	completed, ok := f.manager.agent.lookupTask(task.ID, task.Account, task.Thread)
	if !ok || completed.State != "completed" || !strings.Contains(completed.LastResult, "微信服务端接收回执") || strings.Contains(completed.LastResult, "不代表") {
		t.Fatalf("verified receipt described as unverified: %+v", completed)
	}
}
