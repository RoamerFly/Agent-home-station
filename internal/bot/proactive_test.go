package bot

import (
	"context"
	"testing"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type proactiveModel struct {
	calls  int
	answer string
}

func (m *proactiveModel) Chat(context.Context, string, []map[string]string) (string, error) {
	m.calls++
	return m.answer, nil
}

func TestProactiveWorkerSendsToSelectedGroupAndFriendOrChoosesSilence(t *testing.T) {
	for _, thread := range []string{"room@chatroom", "friend"} {
		for _, silence := range []bool{false, true} {
			t.Run(thread+map[bool]string{true: "-silent", false: "-reply"}[silence], func(t *testing.T) {
				c := companion.Defaults()
				c.Enabled = true
				c.Permissions.Proactive = true
				c.Permissions.Remember = true
				c.QuietStart = "00:00"
				c.QuietEnd = "00:00"
				c.ProactiveThreads = []string{thread}
				c.ProactiveIdleMinutes = 5
				c.ReplyMinMS = 0
				c.ReplyMaxMS = 0
				store, _ := companion.NewStore(t.TempDir())
				if opened := store; opened != nil {
					t.Cleanup(func() { _ = opened.Close() })
				}
				_ = store.Observe(companion.Input{Channel: "wechat", Account: "test", Thread: thread, Text: "刚才聊的音乐"}, c, time.Now().Add(-time.Hour))
				m := &proactiveModel{answer: `{"action":"reply","messages":["刚才那首歌挺轻快的～"]}`}
				if silence {
					m.answer = `{"action":"silent","messages":[]}`
				}
				engine, _ := companion.New(c, store, m, "test")
				sender := &fakeSender{}
				worker, err := New(Config{Channel: "wechat", StateDir: t.TempDir(), AllowedThreads: []string{thread}}, &fakeSource{}, engine, sender)
				if err != nil {
					t.Fatal(err)
				}
				worker.tickCharacter(context.Background())
				worker.tickCharacter(context.Background())
				if m.calls != 1 {
					t.Fatal("claim repeated or not scheduled", m.calls)
				}
				want := 1
				if silence {
					want = 0
				}
				if sender.calls != want {
					t.Fatal("wrong sends", sender.calls)
				}
				if want == 1 && sender.requests[0].Recipient != thread {
					t.Fatal("wrong destination")
				}
				_, used := store.Counters(c, time.Now())
				if used != 1 {
					t.Fatal("silent/reply claim not persisted", used)
				}
			})
		}
	}
}

func TestUntriggeredGroupActivityDoesNotPostponeAutonomyOrMemorizeBody(t *testing.T) {
	c := companion.Defaults()
	c.Enabled = true
	c.Permissions.Remember = true
	c.Permissions.Proactive = true
	c.QuietStart = "00:00"
	c.QuietEnd = "00:00"
	c.ProactiveThreads = []string{"room@chatroom"}
	c.ProactiveIdleMinutes = 5
	store, _ := companion.NewStore(t.TempDir())
	if opened := store; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	input := companion.Input{Channel: "wechat", Account: "test", Thread: "room@chatroom", Text: "原有上下文"}
	_ = store.Observe(input, c, time.Now().Add(-time.Hour))
	m := &proactiveModel{answer: `{"action":"silent","messages":[]}`}
	engine, _ := companion.New(c, store, m, "test")
	message := fixtureMessage("ambient", "room@chatroom", "群友互聊，不需要回复", "incoming")
	source := &fakeSource{messages: map[string]linuxnative.Message{"ambient": message}}
	worker, _ := New(Config{Channel: "wechat", StateDir: t.TempDir(), AllowedThreads: []string{"room@chatroom"}, GroupRules: map[string]GroupRule{"room@chatroom": {Mode: "mention"}}}, source, engine, &fakeSender{})
	worker.handle(context.Background(), fixtureEvent("1", "ambient", "room@chatroom", "incoming", "remote"))
	worker.tickCharacter(context.Background())
	if m.calls != 1 {
		t.Fatal("active group did not get autonomous evaluation")
	}
	scope := store.Context(input, c, time.Now())
	if len(scope.Messages) != 1 || scope.Messages[0].Text != "原有上下文" || scope.LastActivity.IsZero() {
		t.Fatal("ambient body memorized or activity missing", scope)
	}
}
