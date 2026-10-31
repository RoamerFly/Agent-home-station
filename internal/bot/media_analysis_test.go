package bot

import (
	"context"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type sourceCheckingAnalyzer func(context.Context, linuxnative.Message) (string, error)

func (f sourceCheckingAnalyzer) Analyze(ctx context.Context, m linuxnative.Message) (string, error) {
	return f(ctx, m)
}

func TestMediaAnalysisPersistsOriginalBeforeParsingAndReusesIdentity(t *testing.T) {
	w, e, _ := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) {
		t.Fatal("unexpected model call")
		return "", nil
	}))
	m := fixtureMessage("incoming-share-source", "filehelper", "", "incoming")
	m.Type, m.AccountID, m.SenderID = 49, w.source.Account(), "synthetic-uploader"
	kind := 51
	m.AppMessageType = &kind
	var first companion.SourceRef
	calls := 0
	w.config.Analyzer = sourceCheckingAnalyzer(func(ctx context.Context, current linuxnative.Message) (string, error) {
		calls++
		ref, ok := companion.MediaSourceFromContext(ctx)
		if !ok || ref.MessageID != current.ID || ref.Sender != m.SenderID || ref.Account != m.AccountID {
			t.Fatal("missing native identity before parser", ref)
		}
		ev, err := e.Store.ReadEvent(ctx, []string{companion.ScopeID(w.config.Channel, m.AccountID, m.ThreadID)}, ref.EventID)
		if err != nil || ev.Text != "" {
			t.Fatal("media observation became original user text", err)
		}
		if first.EventID == "" {
			first = ref
		} else if first.EventID != ref.EventID || first.Revision != ref.Revision {
			t.Fatal("parser retry changed event")
		}
		return "synthetic actual observation", nil
	})
	for range 2 {
		if text, err := w.analyzeIncomingMedia(t.Context(), m); err != nil || text != "synthetic actual observation" {
			t.Fatal(text, err)
		}
	}
	original := ""
	in := companion.Input{Channel: w.config.Channel, Account: m.AccountID, Thread: m.ThreadID, Sender: m.SenderID, TraceID: m.ID, MessageTime: m.CreatedAt, Text: "synthetic actual observation", OriginalText: &original}
	bindInputSource(&in, m, e)
	if err := e.Store.Observe(in, e.Config, time.Now()); err != nil {
		t.Fatal(err)
	}
	ref, ok := e.Store.SourceForInput(in)
	if !ok || ref.EventID != first.EventID || ref.Revision != first.Revision || calls != 2 {
		t.Fatal("later reply did not reuse media source", ref)
	}
}

func TestMediaAnalysisRejectsForgottenAndForeignSources(t *testing.T) {
	w, e, _ := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) { return "", nil }))
	m := fixtureMessage("forgotten-share", "filehelper", "", "incoming")
	m.Type, m.AccountID = 49, w.source.Account()
	calls := 0
	w.config.Analyzer = sourceCheckingAnalyzer(func(ctx context.Context, _ linuxnative.Message) (string, error) {
		calls++
		if err := e.Store.Clear(companion.ScopeID(w.config.Channel, m.AccountID, m.ThreadID)); err != nil {
			t.Fatal(err)
		}
		return "must not expose this result", nil
	})
	if text, err := w.analyzeIncomingMedia(t.Context(), m); err == nil || text != "" {
		t.Fatal("forgotten result escaped")
	}
	if _, err := w.analyzeIncomingMedia(t.Context(), m); err == nil || calls != 1 {
		t.Fatal("forgotten source reparsed", calls, err)
	}
	m.AccountID = "other-account"
	if _, err := w.analyzeIncomingMedia(t.Context(), m); err == nil || calls != 1 {
		t.Fatal("cross account media reached parser")
	}
}

func TestMediaAnalysisWithoutMemoryDoesNotPersistOrInheritProof(t *testing.T) {
	w, e, _ := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) { return "", nil }))
	e.Config.Permissions.Remember = false
	m := fixtureMessage("ephemeral-share", "filehelper", "", "incoming")
	m.Type = 49
	w.config.Analyzer = sourceCheckingAnalyzer(func(ctx context.Context, _ linuxnative.Message) (string, error) {
		if _, ok := companion.MediaSourceFromContext(ctx); ok {
			t.Fatal("inherited unrelated media source")
		}
		return "ephemeral observation", nil
	})
	ctx := companion.WithMediaSource(t.Context(), companion.SourceRef{EventID: "other", Revision: 1})
	if text, err := w.analyzeIncomingMedia(ctx, m); err != nil || text != "ephemeral observation" {
		t.Fatal(text, err)
	}
	in := companion.Input{Channel: w.config.Channel, Account: w.source.Account(), Thread: m.ThreadID, TraceID: m.ID, Sender: m.SenderID}
	if _, found := e.Store.SourceForInput(in); found {
		t.Fatal("remember disabled but media persisted")
	}
}
