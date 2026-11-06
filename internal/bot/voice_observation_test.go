package bot

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type scopedVoiceAnalyzer func(context.Context, linuxnative.Message) (string, error)

func (a scopedVoiceAnalyzer) Analyze(ctx context.Context, m linuxnative.Message) (string, error) {
	return a(ctx, m)
}

func (scopedVoiceAnalyzer) SupportsVoice(_ context.Context, m linuxnative.Message) bool {
	return m.Type == 34 && m.ID == "voice-sample"
}

func TestScopedVoiceObservationReachesPrivateAndGroupedTurns(t *testing.T) {
	for _, scenario := range []struct {
		name, thread   string
		group, backlog bool
	}{
		{name: "private", thread: "friend"},
		{name: "group live window", thread: "room@chatroom", group: true},
		{name: "group after reconnect", thread: "room@chatroom", group: true, backlog: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			const spoken = "请把合成记号念出来"
			var modelCalls, reads atomic.Int32
			w, _, _ := backgroundWorker(t, generationModelFunc(func(_ context.Context, _ string, messages []map[string]string) (string, error) {
				modelCalls.Add(1)
				if !strings.Contains(messages[len(messages)-1]["content"], spoken) {
					t.Error("model did not receive actual voice transcription")
				}
				return `{"action":"silent"}`, nil
			}))
			voice := fixtureMessage("voice-sample", scenario.thread, "[语音]", "incoming")
			voice.SenderID, voice.ServerID, voice.Type = "speaker", "9001", 34
			w.config.VoiceAnalyzer = scopedVoiceAnalyzer(func(ctx context.Context, m linuxnative.Message) (string, error) {
				reads.Add(1)
				ref, ok := companion.MediaSourceFromContext(ctx)
				if !ok || ref.MessageID != m.ID || ref.Sender != m.SenderID || ref.Thread != scenario.thread || ref.MessageType != 34 {
					t.Error("voice analyzer missing persisted source identity")
				}
				return spoken, nil
			})
			w.config.GroupRules = map[string]GroupRule{scenario.thread: {Mode: "all"}}
			w.source = &fakeSource{messages: map[string]linuxnative.Message{voice.ID: voice}}
			event := fixtureEvent("1", voice.ID, scenario.thread, "incoming", "remote")
			w.runtime.Inbox = append(w.runtime.Inbox, inboxItem{Event: event, Phase: "received", Backlog: scenario.backlog})
			w.enableGenerations()
			defer w.closeGenerations()
			switch {
			case scenario.backlog:
				if err := w.processUnreadConversation(t.Context(), scenario.thread); err != nil {
					t.Fatal(err)
				}
			case scenario.group:
				if err := w.processConversationBatch(t.Context(), scenario.thread, map[string]bool{voice.ID: true}); err != nil {
					t.Fatal(err)
				}
			default:
				w.handle(t.Context(), event)
			}
			pumpGeneration(t, w, func() bool { return w.generation.occupied() == 0 })
			if reads.Load() != 1 || modelCalls.Load() != 1 {
				t.Fatalf("voice reads=%d decisions=%d, want one of each", reads.Load(), modelCalls.Load())
			}
		})
	}
}
