package bot

import (
	"context"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

// A public video read may finish just as retained conversation evidence
// changes. Resume its unsent model turn from the recorded tool results. The
// original download is not submitted again, and every new model call receives
// current source and permission checks.
func (w *Worker) resumePausedPublicVideo(ctx context.Context, item *inboxItem, automatic bool) (bool, error) {
	if w == nil || w.runtime == nil || item == nil || item.Phase != "needs_review" || item.RecoveryEvidence == nil || len(item.RecoveryTools) == 0 || item.RecoveryPrivacy != nil {
		return false, nil
	}
	if automatic && (item.EvidenceResumeAttempted || item.Updated.IsZero() || time.Since(item.Updated) > time.Hour) {
		return false, nil
	}
	completedDownload := false
	for _, result := range item.RecoveryTools {
		if result.CallKey == "" || result.ResultID == "" {
			return false, nil
		}
		switch result.Tool {
		case "video_inspect":
			if !result.ReadOnly {
				return false, nil
			}
		case "web_media_download":
			if result.ReadOnly {
				return false, nil
			}
			completedDownload = true
		default:
			return false, nil
		}
	}
	if !completedDownload {
		return false, nil
	}
	previous := item.Event.Data
	thread, trace := previous.ThreadID, previous.MessageID
	if thread == "" || trace == "" || !w.allowed[thread] || w.roundClosed(thread, trace) || replayHasEffects(w.runtime, trace) {
		return false, nil
	}
	if w.generation != nil && w.generation.jobs[thread] != nil {
		return false, nil
	}
	backlogHours := 24
	if w.config.Options != nil && w.config.Options.BacklogHours > 0 {
		backlogHours = w.config.Options.BacklogHours
	}
	if item.Updated.IsZero() || time.Since(item.Updated) > time.Duration(backlogHours)*time.Hour {
		return false, nil
	}
	readCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	current, err := w.source.Message(readCtx, trace)
	cancel()
	if err != nil || !w.samePausedVideoSource(ctx, previous.MessageID, previous.ThreadID, previous.Direction, current) {
		return false, nil
	}
	before := *item
	item.EvidenceResumeAttempted = true
	item.RecoveryEvidence = nil // never reuse an invalidated generation stamp
	item.Phase = "retrying_model"
	item.RetryAfter = time.Time{}
	item.Updated = time.Now().UTC()
	if err := w.saveRuntime(); err != nil {
		*item = before
		return false, err
	}
	w.activity(Record{Event: "generation_resume_queued", Category: "decision", ThreadID: thread, MessageID: trace, Status: "retrying_model", Reason: "reuse_completed_public_video_results", Origin: "agent"})
	return true, w.persistErr
}

func (w *Worker) samePausedVideoSource(ctx context.Context, messageID, thread, direction string, current linuxnative.Message) bool {
	if messageID == "" || current.ID != messageID || current.ThreadID != thread || current.Direction != "incoming" || direction != "incoming" || !w.authorizedMessage(current) {
		return false
	}
	e, ok := w.model.(*companion.Engine)
	if !ok || e.Store == nil {
		return false
	}
	observed := messageSource(current)
	observed.InstanceID = e.Store.SourceInstanceID()
	in := companion.Input{Channel: w.config.Channel, Account: current.AccountID, Thread: current.ThreadID, Sender: current.SenderID, TraceID: current.ID, Source: &observed}
	known, ok := e.Store.SourceForInput(in)
	if !ok || known == nil || known.EventID == "" || known.BodyHash == "" || known.BodyHash != observed.BodyHash || known.Sender != current.SenderID || known.Account != current.AccountID || known.Thread != thread || known.MessageID != messageID {
		return false
	}
	stored, err := e.Store.ReadEvent(ctx, []string{companion.ScopeID(in.Channel, in.Account, in.Thread)}, known.EventID)
	return err == nil && stored.Role == "user" && stored.Trace == messageID && stored.Sender == current.SenderID && stored.Source != nil && stored.Source.BodyHash == observed.BodyHash
}

// On startup, migrate at most one recent, safely checkpointed turn per chat.
// The persisted one-shot bit prevents restart loops if its new model turn
// cannot finish.
func (w *Worker) resumePausedPublicVideos(ctx context.Context) error {
	if w.runtime == nil {
		return nil
	}
	queued := map[string]bool{}
	for i := len(w.runtime.Inbox) - 1; i >= 0; i-- {
		item := &w.runtime.Inbox[i]
		thread := item.Event.Data.ThreadID
		if queued[thread] {
			continue
		}
		ok, err := w.resumePausedPublicVideo(ctx, item, true)
		if err != nil {
			return err
		}
		if ok {
			queued[thread] = true
		}
	}
	return nil
}
