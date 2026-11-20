package bot

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"wechat-bot/internal/linuxnative"
)

func onDemandFile(m linuxnative.Message) bool {
	return m.Direction == "incoming" && m.Type == 49 && m.FileAttachmentName() != ""
}

// An ordinary attachment is a tool-addressable input, not a request to run
// every installed media analyzer. The model still gets a normal turn and can
// choose to use the attachment tool when it actually needs the original.
func TestOnDemandAttachmentStartsPrivateTurnWithoutGenericObservation(t *testing.T) {
	var modelCalls atomic.Int32
	w, _, n := backgroundWorker(t, generationModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
		modelCalls.Add(1)
		return `{"action":"silent"}`, nil
	}))
	analyzer := &receivedFileAnalyzer{}
	w.config.Analyzer = analyzer
	w.config.OnDemandAttachment = onDemandFile
	m := fileObservationMessage(true)
	w.source = &fakeSource{messages: map[string]linuxnative.Message{m.ID: m}}
	w.enableGenerations()
	defer w.closeGenerations()

	w.handle(context.Background(), fixtureEvent("1", m.ID, m.ThreadID, "incoming", "remote"))
	pumpGeneration(t, w, func() bool { return w.generation.occupied() == 0 })
	if analyzer.calls.Load() != 0 {
		t.Fatalf("on-demand attachment unexpectedly used generic analyzer: %d", analyzer.calls.Load())
	}
	if modelCalls.Load() != 1 || len(n.submissions) != 0 {
		t.Fatalf("file turn was not model-decided exactly once: model=%d sent=%d", modelCalls.Load(), len(n.submissions))
	}
}

func TestOnDemandAttachmentStartsSingleGroupTurnWithoutGenericObservation(t *testing.T) {
	for _, mode := range []string{"sync", "background"} {
		t.Run(mode, func(t *testing.T) {
			var modelCalls atomic.Int32
			w, _, n := backgroundWorker(t, generationModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
				modelCalls.Add(1)
				for _, turn := range turns {
					if strings.Contains(turn["content"], `"kind":"current_message"`) && strings.Contains(turn["content"], `"kind":"file"`) &&
						strings.Contains(turn["content"], `"reported_filename":"notes.txt"`) && strings.Contains(turn["content"], "wechat_file_read") {
						return `{"action":"silent"}`, nil
					}
				}
				t.Error("the current message lost its typed file reference")
				return `{"action":"silent"}`, nil
			}))
			m := fileObservationMessage(true)
			m.ThreadID = "room@chatroom"
			w.allowed[m.ThreadID] = true
			w.config.GroupRules = map[string]GroupRule{m.ThreadID: {Mode: "all"}}
			w.config.OnDemandAttachment = onDemandFile
			w.source = &fakeSource{messages: map[string]linuxnative.Message{m.ID: m}}
			w.runtime.Inbox = []inboxItem{{Event: fixtureEvent("1", m.ID, m.ThreadID, "incoming", "remote"), Phase: "received"}}
			if mode == "background" {
				w.enableGenerations()
				defer w.closeGenerations()
			}
			if err := w.drainInbox(context.Background()); err != nil {
				t.Fatal(err)
			}
			if mode == "background" {
				pumpGeneration(t, w, func() bool { return w.generation.occupied() == 0 })
			}
			if modelCalls.Load() != 1 || w.runtime.Inbox[0].Phase != "silent" || len(n.submissions) != 0 {
				t.Fatalf("file did not reach the model and settle silently: model=%d phase=%s sent=%d", modelCalls.Load(), w.runtime.Inbox[0].Phase, len(n.submissions))
			}
		})
	}
}

func TestOnDemandAttachmentStaysInMixedGroupTurnWithoutMandatoryObservation(t *testing.T) {
	var modelCalls atomic.Int32
	w, _, _ := backgroundWorker(t, generationModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
		modelCalls.Add(1)
		joined := ""
		for _, turn := range turns {
			joined += turn["content"]
		}
		if !strings.Contains(joined, "请看这个文件") {
			t.Error("group text context was lost")
		}
		return `{"action":"silent"}`, nil
	}))
	analyzer := &receivedFileAnalyzer{}
	w.config.Analyzer = analyzer
	w.config.OnDemandAttachment = onDemandFile
	room := "room@chatroom"
	w.config.GroupRules = map[string]GroupRule{room: {Mode: "all"}}
	text := fixtureMessage("group-text", room, "请看这个文件", "incoming")
	text.SenderID = "member"
	file := fileObservationMessage(true)
	file.ID, file.ThreadID, file.SenderID = "group-file", room, "member"
	w.source = &fakeSource{messages: map[string]linuxnative.Message{text.ID: text, file.ID: file}}
	w.runtime.Inbox = []inboxItem{
		{Event: fixtureEvent("1", text.ID, room, "incoming", "remote"), Phase: "received"},
		{Event: fixtureEvent("2", file.ID, room, "incoming", "remote"), Phase: "received"},
	}
	w.enableGenerations()
	defer w.closeGenerations()
	if err := w.processConversationBatch(context.Background(), room, map[string]bool{text.ID: true, file.ID: true}); err != nil {
		t.Fatal(err)
	}
	pumpGeneration(t, w, func() bool { return w.generation.occupied() == 0 })
	if analyzer.calls.Load() != 0 {
		t.Fatalf("group on-demand attachment unexpectedly used generic analyzer: %d", analyzer.calls.Load())
	}
	if modelCalls.Load() != 1 {
		t.Fatalf("mixed group turn did not reach model exactly once: %d", modelCalls.Load())
	}
}
