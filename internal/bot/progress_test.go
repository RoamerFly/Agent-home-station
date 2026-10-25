package bot

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
)

type acceptingProgressNative struct{ reliableFake }

func (n *acceptingProgressNative) Submit(_ context.Context, r linuxnative.ToolSend) (linuxnative.ToolStatus, error) {
	n.submissions = append(n.submissions, r)
	s := linuxnative.ToolStatus{ID: r.IdempotencyKey, State: "accepted", ThreadID: r.ThreadID, ServerID: "123"}
	n.intents[r.IdempotencyKey] = s
	return s, nil
}

type progressSequence struct {
	step        int
	finalSilent bool
}

func (m *progressSequence) Chat(context.Context, string, []map[string]string) (string, error) {
	m.step++
	switch m.step {
	case 1:
		return `{"action":"tool","tool":"web_search","progress":"收到，我看看～","arguments":{"query":"source one"}}`, nil
	case 2:
		return `{"action":"tool","tool":"web_search","progress":"还在找","arguments":{"query":"source two"}}`, nil
	default:
		if m.finalSilent {
			return `{"action":"silent","messages":[]}`, nil
		}
		return `{"action":"reply","messages":["找到这个来源了 https://example.org/"]}`, nil
	}
}

func TestAcknowledgedToolTaskAlwaysReportsItsTerminalOutcome(t *testing.T) {
	c := companion.Defaults()
	c.Enabled = true
	c.ReplyMinMS = 0
	c.ReplyMaxMS = 0
	store, _ := companion.NewStore(t.TempDir())
	model := &progressSequence{finalSilent: true}
	engine, _ := companion.New(c, store, model, "test")
	native := &acceptingProgressNative{reliableFake: reliableFake{intents: map[string]linuxnative.ToolStatus{}}}
	o := DefaultOptions()
	o.RandomDelay = false
	w, _ := New(Config{StateDir: t.TempDir(), AllowedThreads: []string{"filehelper"}, Options: &o}, &fakeSource{}, engine, native)
	_ = w.loadRuntime("server-1")
	w.runtime.Inbox = []inboxItem{{Event: fixtureEvent("1", "terminal", "filehelper", "incoming", "remote"), Phase: "processing"}}
	engine.Tools = &checkedProgressTools{before: func() {}}
	engine.MaxToolCalls = 3
	w.respondCharacter(context.Background(), engine, companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "terminal", Mode: "direct", Text: "查一下这个来源"})
	for _, p := range w.runtime.Plans {
		p.InboxChecked = true
	}
	w.reconcileUtterancePlans(context.Background())
	if len(native.submissions) != 2 || !strings.Contains(native.submissions[1].Text, "先停下") || w.runtime.Inbox[0].Phase != "replied" {
		t.Fatal("verified acknowledgement was left without a final outcome", native.submissions, w.runtime.Inbox)
	}
}

type checkedProgressTools struct {
	before func()
	calls  int
}

func (*checkedProgressTools) List(context.Context) ([]mcptools.Tool, error) {
	return []mcptools.Tool{{Name: "web_search"}}, nil
}
func (t *checkedProgressTools) Call(context.Context, string, json.RawMessage) (string, error) {
	t.before()
	t.calls++
	return "https://example.org/", nil
}

func TestProgressHasOwnDurableReceiptAndDoesNotCompleteTask(t *testing.T) {
	for _, ack := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[ack], func(t *testing.T) {
			c := companion.Defaults()
			c.Enabled = true
			c.ReplyMinMS = 0
			c.ReplyMaxMS = 0
			c.Permissions.Remember = true
			store, _ := companion.NewStore(t.TempDir())
			model := &progressSequence{}
			engine, _ := companion.New(c, store, model, "test")
			native := &acceptingProgressNative{reliableFake: reliableFake{intents: map[string]linuxnative.ToolStatus{}}}
			options := DefaultOptions()
			options.AcknowledgeTools = ack
			options.RandomDelay = false
			w, err := New(Config{Channel: "wechat", StateDir: t.TempDir(), AllowedThreads: []string{"filehelper"}, Options: &options}, &fakeSource{}, engine, native)
			if err != nil {
				t.Fatal(err)
			}
			_ = w.loadRuntime("server-1")
			w.runtime.Inbox = []inboxItem{{Event: fixtureEvent("1", "incoming", "filehelper", "incoming", "remote"), Phase: "processing"}}
			tools := &checkedProgressTools{before: func() {
				want := 0
				if ack {
					want = 1
				}
				if len(native.submissions) != want || w.runtime.Inbox[0].Phase != "tool_running" || !w.runtime.Inbox[0].ToolStarted {
					t.Fatal("tool ran before acknowledgement or task already completed", len(native.submissions), w.runtime.Inbox[0])
				}
			}}
			engine.Tools = tools
			engine.MaxToolCalls = 3
			w.respondCharacter(context.Background(), engine, companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "incoming", Mode: "direct", Text: "请查一下这个来源"})
			for _, p := range w.runtime.Plans {
				p.InboxChecked = true
			}
			w.reconcileUtterancePlans(context.Background())
			want := 1
			if ack {
				want = 2
			}
			if tools.calls != 2 || len(native.submissions) != want || len(w.runtime.Deliveries) != want || w.runtime.Inbox[0].Phase != "replied" {
				t.Fatal("final result not independently sent", len(native.submissions), w.runtime.Inbox[0])
			}
			if ack && (w.runtime.Deliveries[0].Bubble != -1 || w.runtime.Deliveries[1].Bubble != 0 || native.submissions[0].IdempotencyKey == native.submissions[1].IdempotencyKey) {
				t.Fatal("progress and outcome reused a delivery slot")
			}
		})
	}
}

func TestInterruptedToolJobIsTerminalInsteadOfReplayingSideEffects(t *testing.T) {
	source := &fakeSource{}
	model := &fakeModel{answer: "must not run"}
	w := newTestWorker(t, source, model, &fakeSender{})
	o := DefaultOptions()
	w.config.Options = &o
	_ = w.loadRuntime("server-1")
	w.runtime.Inbox = []inboxItem{{Event: fixtureEvent("1", "old-tool", "filehelper", "incoming", "remote"), Phase: "tool_running", ToolStarted: true, Updated: time.Now()}}
	if err := w.saveRuntime(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source.onPage = func(string) { cancel() }
	state := checkpoint{ServerID: "server-1", Account: "test", Cursor: "1"}
	if err := w.runDurable(ctx, filepath.Join(w.config.StateDir, "checkpoint.json"), state); err != nil {
		t.Fatal(err)
	}
	if model.calls != 0 || w.runtime.Inbox[0].Phase != "interrupted" {
		t.Fatal("interrupted tool was replayed", model.calls, w.runtime.Inbox)
	}
}

func TestUnknownProgressNeverResendsOrOverridesFinalTaskStatus(t *testing.T) {
	n := &reliableFake{intents: map[string]linuxnative.ToolStatus{}}
	w := durableWorker(t, n)
	w.runtime.Inbox = []inboxItem{{Event: fixtureEvent("1", "x", "filehelper", "incoming", "remote"), Phase: "replied", ToolStarted: true}}
	_, _ = w.sendText(context.Background(), companion.Input{Thread: "filehelper", TraceID: "x", Mode: "direct"}, -1, "收到")
	w.runtime.Inbox[0].Phase = "replied"
	w.runtime.Deliveries[0].Next = time.Time{}
	w.reconcileDeliveries(context.Background())
	if w.runtime.Deliveries[0].State == "stopped" {
		t.Fatal("stopped checking acknowledgement too early")
	}
	w.runtime.Deliveries[0].Created = time.Now().Add(-6 * time.Minute)
	w.runtime.Deliveries[0].Next = time.Time{}
	w.reconcileDeliveries(context.Background())
	if len(n.submissions) != 1 || w.runtime.Inbox[0].Phase != "replied" || !strings.Contains(w.runtime.Deliveries[0].State, "stopped") {
		t.Fatal("progress replayed or changed final status")
	}
}

func TestUnknownProgressCanBeConfirmedBySyncedMessageWithoutResend(t *testing.T) {
	n := &reliableFake{intents: map[string]linuxnative.ToolStatus{}}
	w := durableWorker(t, n)
	_, _ = w.sendText(context.Background(), companion.Input{Thread: "filehelper", TraceID: "ack-sync", Mode: "direct"}, -1, "收到，我来做一首～")
	n.rows = []map[string]any{{"direction": "outgoing", "body": "收到，我来做一首～", "serverId": "12345", "createdAt": time.Now().Format(time.RFC3339)}}
	d := w.runtime.Deliveries[0]
	d.Next = time.Time{}
	w.reconcileDeliveries(context.Background())
	if d.State != "accepted" || len(n.submissions) != 1 {
		t.Fatal("sync receipt not used", d.State, len(n.submissions))
	}
}
