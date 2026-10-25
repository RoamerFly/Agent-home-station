package bot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/sending"
)

type fakeSource struct {
	events   []linuxnative.Event
	messages map[string]linuxnative.Message
	reads    int
	onPage   func(string)
}

func (*fakeSource) Account() string                          { return "test" }
func (*fakeSource) Identity(context.Context) (string, error) { return "server-1", nil }
func (s *fakeSource) Events(_ context.Context, after string) (linuxnative.EventPage, error) {
	if s.onPage != nil {
		s.onPage(after)
	}
	n, _ := strconv.Atoi(after)
	p := linuxnative.EventPage{NextCursor: after}
	for _, e := range s.events {
		v, _ := strconv.Atoi(e.Cursor)
		if v > n {
			p.Items = append(p.Items, e)
			p.NextCursor = e.Cursor
		}
	}
	return p, nil
}
func (s *fakeSource) Message(_ context.Context, id string) (linuxnative.Message, error) {
	s.reads++
	m, ok := s.messages[id]
	if !ok {
		return m, errors.New("missing fixture")
	}
	return m, nil
}

type fakeModel struct {
	calls  int
	answer string
	err    error
	before func(string)
}

func (m *fakeModel) Complete(_ context.Context, prompt string) (string, error) {
	m.calls++
	if m.before != nil {
		m.before(prompt)
	}
	return m.answer, m.err
}

type fakeSender struct {
	calls    int
	err      error
	requests []sending.TextRequest
	after    func()
}

func (*fakeSender) Ready() bool { return true }
func (s *fakeSender) SendText(_ context.Context, r sending.TextRequest) (sending.Receipt, error) {
	s.calls++
	s.requests = append(s.requests, r)
	if s.after != nil {
		s.after()
	}
	return sending.Receipt{Recipient: r.Recipient, ClientMessageID: "1", ServerMessageID: "2"}, s.err
}

func fixtureMessage(id, thread, body, direction string) linuxnative.Message {
	return linuxnative.Message{ID: id, AccountID: "test", ThreadID: thread, Body: body, Direction: direction, Type: 1, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
}
func fixtureEvent(cursor, id, thread, direction, origin string) linuxnative.Event {
	e := linuxnative.Event{Cursor: cursor, Kind: "message.committed"}
	e.Data.MessageID = id
	e.Data.ThreadID = thread
	e.Data.Direction = direction
	e.Data.Origin = origin
	return e
}
func newTestWorker(t *testing.T, s *fakeSource, m *fakeModel, sender *fakeSender) *Worker {
	t.Helper()
	w, err := New(Config{StateDir: t.TempDir(), AllowedThreads: []string{"filehelper", "room@chatroom", "friend"}, PollInterval: time.Millisecond}, s, m, sender)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestEligibilityUsesStructuredMentionsAndFreshAllowlistedText(t *testing.T) {
	w := newTestWorker(t, &fakeSource{}, &fakeModel{}, &fakeSender{})
	yes, no := true, false
	base := fixtureMessage("one", "room@chatroom", "@小号\u2005请回答", "incoming")
	base.Mentions.State = "known"
	base.Mentions.Self = &yes
	for _, tc := range []struct {
		name string
		edit func(*linuxnative.Message)
		want bool
	}{
		{"real mention", func(*linuxnative.Message) {}, true},
		{"typed fake mention", func(m *linuxnative.Message) { m.Mentions.Self = &no }, false},
		{"unknown metadata", func(m *linuxnative.Message) { m.Mentions.State = "unknown" }, false},
		{"missing self", func(m *linuxnative.Message) { m.Mentions.Self = nil }, false},
		{"outgoing group", func(m *linuxnative.Message) { m.Direction = "outgoing" }, false},
		{"wrong thread", func(m *linuxnative.Message) { m.ThreadID = "unapproved@chatroom" }, false},
		{"system message", func(m *linuxnative.Message) { m.Type = 10000 }, false},
		{"stale", func(m *linuxnative.Message) { m.CreatedAt = time.Now().Add(-3 * time.Minute).Format(time.RFC3339) }, false},
		{"future", func(m *linuxnative.Message) { m.CreatedAt = time.Now().Add(time.Minute).Format(time.RFC3339) }, false},
		{"invalid date", func(m *linuxnative.Message) { m.CreatedAt = "bad" }, false},
		{"filehelper trigger", func(m *linuxnative.Message) {
			m.ThreadID = "filehelper"
			m.Direction = "outgoing"
			m.Body = "/ai 问题"
		}, true},
		{"filehelper ordinary", func(m *linuxnative.Message) {
			m.ThreadID = "filehelper"
			m.Direction = "outgoing"
			m.Body = "普通文字"
		}, false},
		{"own AI reply", func(m *linuxnative.Message) {
			m.ThreadID = "filehelper"
			m.Direction = "outgoing"
			m.Body = "[AI] /ai 回答"
		}, false},
		{"direct incoming", func(m *linuxnative.Message) { m.ThreadID = "friend"; m.Body = "/ai 问题" }, true},
		{"direct own", func(m *linuxnative.Message) { m.ThreadID = "friend"; m.Direction = "outgoing"; m.Body = "/ai 问题" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := base
			tc.edit(&m)
			_, got := w.prompt(m, time.Now())
			if got != tc.want {
				t.Fatalf("eligible=%t want=%t", got, tc.want)
			}
		})
	}
}

func TestClaimsBeforeModelAndDoesNotReplayUnknownSendOnRestart(t *testing.T) {
	for _, modelFails := range []bool{false, true} {
		t.Run(strconv.FormatBool(modelFails), func(t *testing.T) {
			s := &fakeSource{events: []linuxnative.Event{fixtureEvent("1", "one", "filehelper", "outgoing", "remote_sync"), fixtureEvent("2", "one", "filehelper", "outgoing", "remote_sync")}, messages: map[string]linuxnative.Message{"one": fixtureMessage("one", "filehelper", "/ai hello", "outgoing")}}
			m := &fakeModel{answer: "回答"}
			sender := &fakeSender{err: sending.ErrOutcomeUnknown}
			w := newTestWorker(t, s, m, sender)
			path := filepath.Join(w.config.StateDir, "checkpoint.json")
			if err := saveCheckpoint(path, checkpoint{Version: 1, ServerID: "server-1", Account: "test", Cursor: "0"}); err != nil {
				t.Fatal(err)
			}
			m.before = func(prompt string) {
				c, _, err := loadCheckpoint(path, "server-1", "test")
				if err != nil || c.Cursor != "1" || !c.contains("one") {
					t.Fatal("external call before durable claim")
				}
				if prompt != "hello" {
					t.Fatalf("prompt=%q", prompt)
				}
			}
			if modelFails {
				m.err = errors.New("failure")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.onPage = func(after string) {
				if after == "2" {
					cancel()
				}
			}
			if err := w.Run(ctx); err != nil {
				t.Fatal(err)
			}
			wantSends := 1
			if modelFails {
				wantSends = 0
			}
			if m.calls != 1 || sender.calls != wantSends || s.reads != 1 {
				t.Fatalf("calls model=%d send=%d reads=%d", m.calls, sender.calls, s.reads)
			}
			ctx2, cancel2 := context.WithCancel(context.Background())
			defer cancel2()
			s.onPage = func(string) { cancel2() }
			w2, err := New(w.config, s, m, sender)
			if err != nil {
				t.Fatal(err)
			}
			if err = w2.Run(ctx2); err != nil {
				t.Fatal(err)
			}
			if m.calls != 1 || sender.calls != wantSends {
				t.Fatal("restart replayed handled message")
			}
		})
	}
}

func TestInitialHistoryIsSkippedWithoutReadingBodies(t *testing.T) {
	s := &fakeSource{events: []linuxnative.Event{fixtureEvent("1", "old", "filehelper", "outgoing", "remote_sync")}}
	m := &fakeModel{}
	sender := &fakeSender{}
	w := newTestWorker(t, s, m, sender)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.onPage = func(after string) {
		if after == "1" {
			cancel()
		}
	}
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if s.reads != 0 || m.calls != 0 || sender.calls != 0 {
		t.Fatal("initial history was processed")
	}
}

func TestLocalSendAndUnapprovedConversationAreNeverRead(t *testing.T) {
	s := &fakeSource{events: []linuxnative.Event{fixtureEvent("1", "one", "filehelper", "outgoing", "local_send"), fixtureEvent("2", "two", "other", "incoming", "remote_sync")}}
	m := &fakeModel{}
	sender := &fakeSender{}
	w := newTestWorker(t, s, m, sender)
	if err := saveCheckpoint(filepath.Join(w.config.StateDir, "checkpoint.json"), checkpoint{Version: 1, ServerID: "server-1", Account: "test", Cursor: "0"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.onPage = func(after string) {
		if after == "2" {
			cancel()
		}
	}
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if s.reads != 0 || m.calls != 0 || sender.calls != 0 {
		t.Fatal("unapproved or local-send message read")
	}
}

func TestOutputIsUTF8BoundedAndHasLoopPreventionPrefix(t *testing.T) {
	s := &fakeSource{messages: map[string]linuxnative.Message{"one": fixtureMessage("one", "filehelper", "/ai hello", "outgoing")}}
	m := &fakeModel{answer: strings.Repeat("中文", 1500)}
	sender := &fakeSender{}
	w := newTestWorker(t, s, m, sender)
	w.handle(context.Background(), fixtureEvent("1", "one", "filehelper", "outgoing", "remote_sync"))
	if sender.calls != 1 {
		t.Fatal("missing send")
	}
	text := sender.requests[0].Text
	if !utf8.ValidString(text) || len(text) > 4096 || !strings.HasPrefix(text, "[AI] ") || !strings.HasSuffix(text, "\n…") {
		t.Fatal("invalid bounded output")
	}
}

func TestCheckpointIdentityCorruptionAndExclusiveLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checkpoint.json")
	c := checkpoint{Version: 1, ServerID: "server-1", Account: "test", Cursor: "9"}
	c.remember("one")
	if err := saveCheckpoint(path, c); err != nil {
		t.Fatal(err)
	}
	for _, identity := range [][2]string{{"another-server", "test"}, {"server-1", "another-account"}} {
		if _, _, err := loadCheckpoint(path, identity[0], identity[1]); err == nil {
			t.Fatal("identity mismatch accepted")
		}
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"cursor":"garbled"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadCheckpoint(path, "server-1", "test"); err == nil {
		t.Fatal("corrupt checkpoint accepted")
	}
	lockPath := filepath.Join(dir, "worker.lock")
	f, err := lockState(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := lockState(lockPath); err == nil {
		other.Close()
		t.Fatal("duplicate worker admitted")
	}
	f.Close()
	f, err = lockState(lockPath)
	if err != nil {
		t.Fatal("lock not released")
	}
	f.Close()
}

func TestPerGroupAllModeAndPrefixAreExplicit(t *testing.T) {
	w := newTestWorker(t, &fakeSource{}, &fakeModel{}, &fakeSender{})
	m := fixtureMessage("one", "room@chatroom", "问：你好", "incoming")
	if _, ok := w.prompt(m, time.Now()); ok {
		t.Fatal("default group rule accepted a non-mention")
	}
	w.config.GroupRules = map[string]GroupRule{"room@chatroom": {Mode: "all", Prefix: "问："}}
	if prompt, ok := w.prompt(m, time.Now()); !ok || prompt != "你好" {
		t.Fatal("explicit group prefix did not apply")
	}
	m.Body = "普通群消息"
	if _, ok := w.prompt(m, time.Now()); ok {
		t.Fatal("group prefix was ignored")
	}
	w.config.GroupRules["room@chatroom"] = GroupRule{Mode: "all"}
	if _, ok := w.prompt(m, time.Now()); !ok {
		t.Fatal("explicit all-message mode not applied")
	}
}
