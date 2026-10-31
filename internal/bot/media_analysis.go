package bot

import (
	"context"
	"errors"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

// Persist the original media identity before invoking an external parser. The
// later observation updates this same source; it is never a new user statement.
// Without memory permission, preserve the existing ephemeral observation path.
func (w *Worker) analyzeIncomingMedia(ctx context.Context, message linuxnative.Message) (string, error) {
	// A batch may reuse the parent context; never inherit another item's proof.
	ctx = companion.WithMediaSource(ctx, companion.SourceRef{})
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if w.config.Analyzer == nil {
		return "", errors.New("media_analyzer_unavailable")
	}
	e, ok := w.model.(*companion.Engine)
	if !ok || e.Store == nil || !e.Config.Permissions.Remember {
		return w.config.Analyzer.Analyze(ctx, message)
	}
	account := w.source.Account()
	if message.AccountID != "" && message.AccountID != account || message.ID == "" || message.ThreadID == "" {
		return "", errors.New("media_source_scope_mismatch")
	}
	original := ""
	in := companion.Input{Channel: w.config.Channel, Account: account, Thread: message.ThreadID, Sender: message.SenderID, TraceID: message.ID, MessageTime: message.CreatedAt, Text: messageSummary(message), OriginalText: &original}
	bindInputSource(&in, message, e)
	source, found := e.Store.SourceForInput(in)
	if !found {
		if err := e.Store.Observe(in, e.Config, time.Now()); err != nil {
			return "", err
		}
		source, found = e.Store.SourceForInput(in)
	}
	if !found {
		return "", errors.New("source_identity_conflict_or_unavailable")
	}
	ctx = companion.WithMediaSource(ctx, *source)
	text, err := w.config.Analyzer.Analyze(ctx, message)
	if err != nil {
		return text, err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	in.Source = source
	if current, valid := e.Store.SourceForInput(in); !valid || current.EventID != source.EventID || current.Revision != source.Revision {
		return "", errors.New("media_source_changed_during_analysis")
	}
	return text, nil
}
