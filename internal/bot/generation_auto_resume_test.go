package bot

import (
	"context"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func pausedPublicVideoFixture(t *testing.T) (*Worker, *inboxItem, linuxnative.Message) {
	t.Helper()
	w := durableWorker(t, &reliableFake{})
	at := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	message := linuxnative.Message{ID: "public-video-followup", AccountID: "test", ThreadID: "room@chatroom", SenderID: "owner", Direction: "incoming", Type: 1, Body: "再读那条 B 站视频", CreatedAt: at}
	message.ServerID = "native-followup"
	w.source = &fakeSource{messages: map[string]linuxnative.Message{message.ID: message}}
	w.allowed[message.ThreadID] = true
	config := companion.Defaults()
	config.Enabled = true
	config.Permissions.Remember = true
	store, err := companion.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	engine, err := companion.New(config, store, &ownerReviewModel{}, "test")
	if err != nil {
		t.Fatal(err)
	}
	w.model = engine
	original := message.Body
	proof := messageSource(message)
	proof.InstanceID = store.SourceInstanceID()
	if err := store.Observe(companion.Input{Channel: "wechat", Account: message.AccountID, Thread: message.ThreadID, Sender: message.SenderID, TraceID: message.ID, Text: message.Body, OriginalText: &original, MessageTime: message.CreatedAt, Source: &proof}, config, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !w.samePausedVideoSource(t.Context(), message.ID, message.ThreadID, message.Direction, message) {
		got, ok := store.SourceForInput(companion.Input{Channel: "wechat", Account: message.AccountID, Thread: message.ThreadID, Sender: message.SenderID, TraceID: message.ID, Source: &proof})
		t.Fatalf("synthetic persisted source unavailable: native=%+v stored=%+v ok=%v", proof, got, ok)
	}
	event := fixtureEvent("1", message.ID, message.ThreadID, "incoming", "remote")
	w.runtime.Inbox = []inboxItem{{Event: event, Phase: "needs_review", ToolStarted: true, Updated: time.Now().UTC(),
		RecoveryEvidence: &companion.GenerationEvidence{RecallEpoch: "old-epoch", Authorization: "old-grant"},
		RecoveryTools: []companion.RecoveryToolResult{
			{Tool: "video_inspect", CallKey: "read-one", ResultID: "saved-read", Result: "已读的片段", ReadOnly: true},
			{Tool: "web_media_download", CallKey: "download-one", ResultID: "saved-download", Result: `{"asset_id":"saved-video"}`},
		}}}
	return w, &w.runtime.Inbox[0], message
}

func TestPausedPublicVideoAutomaticallyResumesOnlyOnce(t *testing.T) {
	w, item, message := pausedPublicVideoFixture(t)
	if err := w.resumePausedPublicVideos(t.Context()); err != nil {
		t.Fatal(err)
	}
	if item.Phase != "retrying_model" || !item.EvidenceResumeAttempted || item.RecoveryEvidence != nil || len(item.RecoveryTools) != 2 || item.RecoveryTools[1].ResultID != "saved-download" {
		t.Fatalf("saved tool result or one-shot continuation lost: %+v", *item)
	}
	if err := w.loadRuntime("server-1"); err != nil {
		t.Fatal(err)
	}
	if got := w.runtime.Inbox[0]; got.Phase != "retrying_model" || !got.EvidenceResumeAttempted || len(got.RecoveryTools) != 2 {
		t.Fatalf("continuation was not durable: %+v", got)
	}
	w.runtime.Inbox[0].Phase = "needs_review"
	if resumed, err := w.resumePausedPublicVideo(t.Context(), &w.runtime.Inbox[0], true); err != nil || resumed {
		t.Fatal("automatic retry loop requeued the same message", resumed, err)
	}
	if w.runtime.Inbox[0].Event.Data.MessageID != message.ID {
		t.Fatal("original message changed")
	}
}

func TestPausedPublicVideoManualResumeRejectsChangedSourceAndDelivery(t *testing.T) {
	w, item, message := pausedPublicVideoFixture(t)
	w.source = &fakeSource{messages: map[string]linuxnative.Message{message.ID: func() linuxnative.Message { changed := message; changed.Body = "另一条请求"; return changed }()}}
	if resumed, err := w.resumePausedPublicVideo(context.Background(), item, false); err != nil || resumed || item.Phase != "needs_review" {
		t.Fatal("changed source resumed old result", resumed, err)
	}
	w.source = &fakeSource{messages: map[string]linuxnative.Message{message.ID: message}}
	w.config.AuthorizeReply = func(companion.Input) bool { return false }
	if resumed, err := w.resumePausedPublicVideo(context.Background(), item, false); err != nil || resumed || item.Phase != "needs_review" {
		t.Fatal("revoked reply authorization resumed old result", resumed, err)
	}
	w.config.AuthorizeReply = nil
	w.runtime.Deliveries = []*delivery{{Trace: message.ID, Thread: message.ThreadID, State: "accepted"}}
	if resumed, err := w.resumePausedPublicVideo(context.Background(), item, false); err != nil || resumed || item.Phase != "needs_review" {
		t.Fatal("existing delivery was regenerated", resumed, err)
	}
}

func TestManualRoundProcessingReusesPausedPublicVideoCheckpoint(t *testing.T) {
	w, item, message := pausedPublicVideoFixture(t)
	result, err := w.triggerRoundProcessing(t.Context(), message.ThreadID, message.ID)
	if err != nil || result.Status != "queued" || item.Phase != "retrying_model" || len(item.RecoveryTools) != 2 {
		t.Fatalf("paused public video was not continued from checkpoint: result=%+v item=%+v err=%v", result, *item, err)
	}
}
