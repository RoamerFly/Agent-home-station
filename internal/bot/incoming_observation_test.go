package bot

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type countingObservationAnalyzer struct{ calls atomic.Int32 }

func (a *countingObservationAnalyzer) Analyze(_ context.Context, m linuxnative.Message) (string, error) {
	a.calls.Add(1)
	return "实际读取：" + m.ID, nil
}

func TestDefaultShareSummaryEnabledUsesExplicitOptOutOnly(t *testing.T) {
	for _, tc := range []struct {
		name  string
		notes string
		want  bool
	}{
		{"enabled", "群收到视频号、快手等分享时，默认总结主要内容并发送。", true},
		{"unrelated negation remains enabled", "总结不必太长；群收到分享时默认总结主要内容。", true},
		{"direct opt out", "不要默认总结群里的分享内容。", false},
		{"default negative", "群分享默认不总结。", false},
		{"not a default", "群里可以聊聊视频分享。", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultShareSummaryEnabled(tc.notes); got != tc.want {
				t.Fatalf("defaultShareSummaryEnabled(%q) = %v, want %v", tc.notes, got, tc.want)
			}
		})
	}
}

func TestMediaReadTimingDefersOrdinaryGroupMediaButKeepsVoiceAndEnabledShare(t *testing.T) {
	w, e, _ := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) {
		return `{"action":"silent"}`, nil
	}))
	// A forkable tool catalogue marks the normal generated path. Without one,
	// the legacy synchronous path intentionally retains eager compatibility.
	e.Tools = &backgroundCapabilityProbe{}
	w.enableGenerations()
	defer w.closeGenerations()
	if w.generation == nil {
		t.Fatal("generated path was not enabled")
	}
	w.config.Options.ReadImages = true
	w.config.Options.ReadVideos = true
	room := "observation@chatroom"
	w.allowed[room] = true

	for _, typ := range []int{3, 43} {
		m := fixtureMessage("ordinary-"+string(rune('0'+typ)), room, "", "incoming")
		m.Type = typ
		if w.readMediaBeforeDecision(m) {
			t.Fatalf("ordinary group media type %d was read before the model could decide", typ)
		}
	}
	voice := fixtureMessage("voice", room, "", "incoming")
	voice.Type = 34
	if !w.readMediaBeforeDecision(voice) {
		t.Fatal("voice command was not transcribed before intent selection")
	}

	e.Config.Permissions.ObserveGroups = true
	e.Config.Permissions.AutonomousGroups = true
	e.Config.Permissions.ReplyText = true
	w.config.Analyzer = unavailableShareAnalyzer{}
	w.config.GroupRules = map[string]GroupRule{room: {Mode: "mention"}}
	seed := companion.Input{Channel: w.config.Channel, Account: w.source.Account(), Thread: room, Sender: "member", TraceID: "pref-seed", Text: "群偏好"}
	if err := e.Store.Observe(seed, e.Config, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.SetPreferences(companion.ScopeID(seed.Channel, seed.Account, room), "总结不必太长；群收到视频号、快手等分享时，默认总结主要内容并发送。", 0); err != nil {
		t.Fatal(err)
	}
	share := fixtureMessage("share", room, "", "incoming")
	share.Type = 49
	share.Link = &linuxnative.LinkCard{URL: "https://www.kuaishou.com/short-video/synthetic"}
	if !w.readMediaBeforeDecision(share) {
		t.Fatal("explicit default-share preference did not request the needed content")
	}
	_, revision := e.Store.Preferences(companion.ScopeID(seed.Channel, seed.Account, room))
	if err := e.Store.SetPreferences(companion.ScopeID(seed.Channel, seed.Account, room), "不要默认总结群里的分享内容。", revision); err != nil {
		t.Fatal(err)
	}
	if w.readMediaBeforeDecision(share) {
		t.Fatal("explicit default-share opt-out was ignored")
	}
}

func TestReadTurnMediaOnlyReadsCurrentQuoteOrUnreadAndReusesCache(t *testing.T) {
	w, e, _ := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) {
		return `{"action":"silent"}`, nil
	}))
	w.config.Options.ReadImages = true
	analyzer := &countingObservationAnalyzer{}
	w.config.Analyzer = analyzer
	room := "media-range@chatroom"
	w.allowed[room] = true
	current := fixtureMessage("current-image", room, "", "incoming")
	current.Type, current.SenderID = 3, "alice"
	unread := fixtureMessage("unread-image", room, "", "incoming")
	unread.Type, unread.SenderID = 3, "bob"
	foreign := fixtureMessage("foreign-image", "other@chatroom", "", "incoming")
	foreign.Type = 3
	w.source = &fakeSource{messages: map[string]linuxnative.Message{current.ID: current, unread.ID: unread, foreign.ID: foreign}}

	observe := func(m linuxnative.Message) companion.SourceRef {
		t.Helper()
		body := ""
		in := companion.Input{Channel: w.config.Channel, Account: w.source.Account(), Thread: m.ThreadID, Sender: m.SenderID, TraceID: m.ID, MessageTime: m.CreatedAt, Text: messageSummary(m), OriginalText: &body}
		bindInputSource(&in, m, e)
		if err := e.Store.Observe(in, e.Config, time.Now()); err != nil {
			t.Fatal(err)
		}
		ref, ok := e.Store.SourceForInput(in)
		if !ok {
			t.Fatal("observed source unavailable")
		}
		return *ref
	}
	currentRef := observe(current)
	unreadRef := observe(unread)
	base := companion.Input{Channel: w.config.Channel, Account: w.source.Account(), Thread: room, Sender: current.SenderID, TraceID: current.ID, MessageTime: current.CreatedAt}

	got, err := w.readTurnMedia(t.Context(), base, nil)
	if err != nil || !strings.Contains(got, "实际读取：current-image") || analyzer.calls.Load() != 1 {
		t.Fatalf("current media = %q, %v, calls=%d", got, err, analyzer.calls.Load())
	}
	// Same source is served from the source-versioned cache rather than reading
	// the image again.
	if _, err = w.readTurnMedia(t.Context(), base, nil); err != nil || analyzer.calls.Load() != 1 {
		t.Fatalf("cached current media = %v, calls=%d", err, analyzer.calls.Load())
	}

	quoted := base
	quoted.TraceID = "reply-to-current"
	quoted.QuotedSource = &currentRef
	if _, err = w.readTurnMedia(t.Context(), quoted, json.RawMessage(`{"message_id":"current-image"}`)); err != nil || analyzer.calls.Load() != 1 {
		t.Fatalf("quoted source was not safely cache-readable: %v calls=%d", err, analyzer.calls.Load())
	}
	unreadInput := base
	unreadInput.UnreadMessages = []companion.ConversationEvent{{MessageID: unread.ID, Source: &unreadRef}}
	if got, err = w.readTurnMedia(t.Context(), unreadInput, json.RawMessage(`{"message_id":"unread-image"}`)); err != nil || !strings.Contains(got, "实际读取：unread-image") || analyzer.calls.Load() != 2 {
		t.Fatalf("unread source = %q, %v, calls=%d", got, err, analyzer.calls.Load())
	}
	if _, err = w.readTurnMedia(t.Context(), base, json.RawMessage(`{"message_id":"foreign-image"}`)); err == nil || !strings.Contains(err.Error(), "not_authorized") || analyzer.calls.Load() != 2 {
		t.Fatalf("foreign source escaped the read range: %v calls=%d", err, analyzer.calls.Load())
	}
}
