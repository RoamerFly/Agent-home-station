package bot

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type scopedTestAnalyzer struct {
	kind    int
	enabled bool
	analyze func(context.Context, linuxnative.Message) (string, error)
}

func (a scopedTestAnalyzer) Analyze(ctx context.Context, m linuxnative.Message) (string, error) {
	return a.analyze(ctx, m)
}

func (a scopedTestAnalyzer) HandlesType(kind int) bool { return kind == a.kind }

func (a scopedTestAnalyzer) SupportsMedia(_ context.Context, m linuxnative.Message) bool {
	return a.enabled && a.HandlesType(m.Type) && m.ID == "media-sample"
}

func TestScopedMediaObservationReachesPrivateAndGroupedTurns(t *testing.T) {
	for _, scenario := range []struct {
		name, thread   string
		group, backlog bool
		kind           int
		disabled       bool
	}{
		{name: "private voice", thread: "friend", kind: 34},
		{name: "voice in group live window", thread: "room@chatroom", group: true, kind: 34},
		{name: "voice in group after reconnect", thread: "room@chatroom", group: true, backlog: true, kind: 34},
		{name: "private image", thread: "friend", kind: 3},
		{name: "image in group live window", thread: "room@chatroom", group: true, kind: 3},
		{name: "image in group after reconnect", thread: "room@chatroom", group: true, backlog: true, kind: 3},
		{name: "image before opt-in", thread: "room@chatroom", group: true, kind: 3, disabled: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			const observation = "合成媒体观察记号"
			var modelCalls, reads atomic.Int32
			w, _, _ := backgroundWorker(t, generationModelFunc(func(_ context.Context, _ string, messages []map[string]string) (string, error) {
				modelCalls.Add(1)
				if strings.Contains(messages[len(messages)-1]["content"], observation) == scenario.disabled {
					t.Error("model received incorrect media observation coverage")
				}
				return `{"action":"silent"}`, nil
			}))
			media := fixtureMessage("media-sample", scenario.thread, "[媒体]", "incoming")
			media.SenderID, media.ServerID, media.Type = "sender", "9001", scenario.kind
			w.config.Options.ReadImages = false // The scoped host, not the legacy option, owns admission.
			w.config.ScopedAnalyzer = scopedTestAnalyzer{kind: scenario.kind, enabled: !scenario.disabled, analyze: func(ctx context.Context, m linuxnative.Message) (string, error) {
				reads.Add(1)
				ref, ok := companion.MediaSourceFromContext(ctx)
				if !ok || ref.MessageID != m.ID || ref.Sender != m.SenderID || ref.Thread != scenario.thread || ref.MessageType != scenario.kind {
					t.Error("media analyzer missing persisted source identity")
				}
				return observation, nil
			}}
			w.config.GroupRules = map[string]GroupRule{scenario.thread: {Mode: "all"}}
			w.source = &fakeSource{messages: map[string]linuxnative.Message{media.ID: media}}
			event := fixtureEvent("1", media.ID, scenario.thread, "incoming", "remote")
			w.runtime.Inbox = append(w.runtime.Inbox, inboxItem{Event: event, Phase: "received", Backlog: scenario.backlog})
			w.enableGenerations()
			defer w.closeGenerations()
			switch {
			case scenario.backlog:
				if err := w.processUnreadConversation(t.Context(), scenario.thread); err != nil {
					t.Fatal(err)
				}
			case scenario.group:
				if err := w.processConversationBatch(t.Context(), scenario.thread, map[string]bool{media.ID: true}); err != nil {
					t.Fatal(err)
				}
			default:
				w.handle(t.Context(), event)
			}
			pumpGeneration(t, w, func() bool { return w.generation.occupied() == 0 })
			wantReads := int32(1)
			if scenario.disabled {
				wantReads = 0
			}
			if reads.Load() != wantReads || modelCalls.Load() != 1 {
				t.Fatalf("media reads=%d decisions=%d, want %d and 1", reads.Load(), modelCalls.Load(), wantReads)
			}
		})
	}
}
