package bot

import (
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func TestTaskDeliveryCountsPartialVoiceAndWaitsForCaption(t *testing.T) {
	w, _, _, in := mediaWorker(t)
	in.TaskID = "song-task"
	var got TaskDelivery
	w.config.AgentTaskDelivery = func(input companion.Input, d TaskDelivery) error {
		if input.TaskID != in.TaskID {
			t.Fatal(input)
		}
		got = d
		return nil
	}
	w.runtime.Media = []*mediaDelivery{
		{Trace: in.TraceID, Input: in, State: "running", Status: linuxnative.ToolStatus{VoiceChunks: []linuxnative.VoiceChunkStatus{{State: "accepted", HasReceipt: true}, {State: "running"}}}},
		{Trace: in.TraceID, Input: in, State: "prepared"},
	}
	w.publishTaskDelivery(in.TraceID)
	if got.State != "partial" || got.AcceptedParts != 1 || got.Expected != 2 {
		t.Fatal(got)
	}
	for _, d := range w.runtime.Media {
		d.State = "accepted"
	}
	w.runtime.MediaReplies = []*mediaReply{{Trace: in.TraceID, Input: in, Expected: 2, State: "waiting_delivery"}}
	w.publishTaskDelivery(in.TraceID)
	if got.State == "accepted" {
		t.Fatal("unfinished caption ignored", got)
	}
	w.runtime.MediaReplies[0].State = "queued_plan"
	w.runtime.Plans = []*utterancePlan{{Input: in, State: "queued", Created: time.Now()}}
	w.publishTaskDelivery(in.TraceID)
	if got.State == "accepted" {
		t.Fatal("queued caption counted as sent")
	}
	w.runtime.Plans[0].State = "accepted"
	w.publishTaskDelivery(in.TraceID)
	if got.State != "accepted" || got.Accepted != 2 {
		t.Fatal(got)
	}
	w.runtime.Media[1].State = "needs_review"
	w.publishTaskDelivery(in.TraceID)
	if got.State != "needs_review" {
		t.Fatal("unknown receipt lost", got)
	}
	w.runtime.Media[1].State = "accepted"
	w.runtime.Plans[0].MediaCaption = true
	w.applyPlanReview(w.runtime.Plans[0], companion.PendingReview{Action: "cancel"}, nil)
	w.publishTaskDelivery(in.TraceID)
	if got.State != "accepted" || !got.CaptionOmitted {
		t.Fatal("voluntary caption silence marked successful media failed", got)
	}
	w.runtime.Plans[0].StopReason = ""
	w.publishTaskDelivery(in.TraceID)
	if got.State != "partial_failed" {
		t.Fatal("actual caption failure hidden", got)
	}
	w.runtime.Plans[0].StopReason = "model_cancel"
	w.runtime.Media[1].State = "needs_review"
	w.publishTaskDelivery(in.TraceID)
	if got.State != "needs_review" {
		t.Fatal("silence invented a missing attachment receipt", got)
	}
}
