package bot

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type receivedFileAnalyzer struct{ calls atomic.Int32 }

func (a *receivedFileAnalyzer) Analyze(_ context.Context, m linuxnative.Message) (string, error) {
	a.calls.Add(1)
	if m.FileAttachmentName() != "notes.txt" {
		panic("expected actual file attachment")
	}
	return "[收到文件原件] notes.txt；asset_id：synthetic-file-observed", nil
}

func fileObservationMessage(structured bool) linuxnative.Message {
	m := fixtureMessage("file-event", "friend", `<msg><appmsg><type>6</type><title>notes.txt</title><appattach><aeskey>SYNTHETIC-PRIVATE-KEY</aeskey></appattach></appmsg></msg>`, "incoming")
	m.Type, m.SenderID = 49, "person"
	if structured {
		subtype := 6
		m.Body, m.AppMessageType = "", &subtype
		m.Attachments = []linuxnative.Attachment{{Kind: "file", Filename: "notes.txt", State: "identified", MessageID: m.ID}}
	}
	return m
}

func TestFileObservationDoesNotDependOnImageOrVideoPermission(t *testing.T) {
	for _, mode := range []string{"sync", "background", "startup"} {
		for _, structured := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/xml", true: "/structured"}[structured], func(t *testing.T) {
				var modelCalls atomic.Int32
				w, c, n := backgroundWorker(t, generationModelFunc(func(_ context.Context, _ string, turns []map[string]string) (string, error) {
					modelCalls.Add(1)
					current := turns[len(turns)-1]["content"]
					if !strings.Contains(current, "synthetic-file-observed") || strings.Contains(current, "收到了一条分享卡片") || strings.Contains(current, "SYNTHETIC-PRIVATE-KEY") {
						t.Error("file was skipped, misclassified, or leaked its raw payload")
					}
					return `{"action":"silent"}`, nil
				}))
				a := &receivedFileAnalyzer{}
				w.config.Analyzer = a
				w.config.Options.ReadImages, w.config.Options.ReadVideos = false, false
				m := fileObservationMessage(structured)
				w.source = &fakeSource{messages: map[string]linuxnative.Message{m.ID: m}}
				w.runtime.Inbox = []inboxItem{{Event: fixtureEvent("1", m.ID, m.ThreadID, "incoming", "remote"), Phase: "received", Backlog: mode == "startup"}}
				if mode != "sync" {
					w.enableGenerations()
					defer w.closeGenerations()
				}
				if err := w.drainInbox(context.Background()); err != nil {
					t.Fatal(err)
				}
				if mode != "sync" {
					pumpGeneration(t, w, func() bool { return w.generation.occupied() == 0 })
				}
				if a.calls.Load() != 1 || modelCalls.Load() != 1 || len(n.submissions) != 0 {
					t.Fatal("not one observed, model-decided, unsent file", a.calls.Load(), modelCalls.Load())
				}
				scope := c.Store.Context(companion.Input{Channel: "wechat", Account: "test", Thread: m.ThreadID}, c.Config, time.Now())
				found := false
				for _, turn := range scope.Messages {
					found = found || turn.TraceID == m.ID && strings.Contains(turn.Text, "synthetic-file-observed")
				}
				if !found || w.runtime.Inbox[0].Phase != "silent" {
					t.Fatal("observed file or durable model decision lost", found, w.runtime.Inbox[0].Phase)
				}
			})
		}
	}
}

func TestFileFollowupIsObservedWithVisionDisabled(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	steps := 0
	w, c, n := backgroundWorker(t, generationModelFunc(func(ctx context.Context, _ string, turns []map[string]string) (string, error) {
		steps++
		if steps == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		} else {
			all := ""
			for _, turn := range turns {
				all += turn["content"]
			}
			if !strings.Contains(all, "synthetic-file-observed") || !strings.Contains(all, "current_followup") {
				t.Error("attachment did not enter the active request")
			}
		}
		return `{"action":"silent"}`, nil
	}))
	a := &receivedFileAnalyzer{}
	w.config.Analyzer = a
	w.config.Options.ReadImages, w.config.Options.ReadVideos = false, false
	m := fileObservationMessage(true)
	w.source = &fakeSource{messages: map[string]linuxnative.Message{m.ID: m}}
	w.enableGenerations()
	defer w.closeGenerations()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: m.ThreadID, Sender: m.SenderID, TraceID: "parent", Mode: "direct", Text: "稍后给你文件"}
	w.runtime.Inbox = []inboxItem{{Event: fixtureEvent("1", in.TraceID, in.Thread, "incoming", "remote"), Phase: "processing"}, {Event: fixtureEvent("2", m.ID, m.ThreadID, "incoming", "remote"), Phase: "received"}}
	w.startGeneration(context.Background(), c, in, nil)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("initial model request did not start")
	}
	w.noticeConversationMessage(m, true)
	if w.runtime.Inbox[1].Phase != "following" {
		t.Fatal("file was not offered as a live followup")
	}
	close(release)
	pumpGeneration(t, w, func() bool { return w.generation.occupied() == 0 })
	if steps != 2 || a.calls.Load() != 1 || w.runtime.Inbox[1].Phase != "completed" || len(n.submissions) != 0 {
		t.Fatal("file followup replayed, skipped, or sent", steps, a.calls.Load(), w.runtime.Inbox[1].Phase)
	}
}

func TestNonFileMediaCannotBorrowFileObservationPermission(t *testing.T) {
	for _, kind := range []int{3, 43, 47, 49} {
		t.Run(strconv.Itoa(kind), func(t *testing.T) {
			w, _, n := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) {
				t.Error("disabled observation reached the model")
				return `{"action":"silent"}`, nil
			}))
			a := &receivedFileAnalyzer{}
			w.config.Analyzer = a
			w.config.Options.ReadImages, w.config.Options.ReadVideos = false, false
			m := fileObservationMessage(true)
			m.Type = kind
			subtype := 74
			m.AppMessageType = &subtype
			m.Body = `<appmsg><type>6</type><title>notes.txt</title></appmsg>`
			w.source = &fakeSource{messages: map[string]linuxnative.Message{m.ID: m}}
			w.handle(context.Background(), fixtureEvent("1", m.ID, m.ThreadID, "incoming", "remote"))
			if a.calls.Load() != 0 || len(n.submissions) != 0 {
				t.Fatal("disabled media used file acquisition permission")
			}
		})
	}
}
