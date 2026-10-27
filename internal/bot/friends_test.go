package bot

import (
	"context"
	"errors"
	"testing"
	"time"
	"wechat-bot/internal/linuxnative"
)

func TestDynamicFriendAdmissionBeforeReadingAndNoPrivatePrefix(t *testing.T) {
	s := &fakeSource{messages: map[string]linuxnative.Message{"one": fixtureMessage("one", "friend", "今天好累", "incoming")}}
	m := &fakeModel{answer: "辛苦了"}
	sender := &fakeSender{}
	w := newTestWorker(t, s, m, sender)
	w.config.AuthorizeThread = func(context.Context, string) (bool, error) { return true, nil }
	w.handle(context.Background(), fixtureEvent("1", "one", "friend", "incoming", "remote_sync"))
	if s.reads != 1 || m.calls != 1 || sender.calls != 1 {
		t.Fatal("verified friend blocked by old allowlist/prefix", s.reads, m.calls, sender.calls)
	}
	for _, failure := range []bool{true, false} {
		s.reads = 0
		m.calls = 0
		sender.calls = 0
		w.config.AuthorizeThread = func(context.Context, string) (bool, error) {
			if failure {
				return false, errors.New("directory offline")
			}
			return false, nil
		}
		w.handle(context.Background(), fixtureEvent("2", "one", "friend", "incoming", "remote_sync"))
		if s.reads != 0 || m.calls != 0 || sender.calls != 0 {
			t.Fatal("unverified contact body read")
		}
	}
}
func TestDynamicFriendOnlyWorkerMayStartWithoutStaticAllowlist(t *testing.T) {
	s := &fakeSource{}
	m := &fakeModel{}
	sender := &fakeSender{}
	_, e := New(Config{StateDir: t.TempDir(), Prefix: "/ai ", MaxAge: time.Minute, AuthorizeThread: func(context.Context, string) (bool, error) { return false, nil }}, s, m, sender)
	if e != nil {
		t.Fatal(e)
	}
}
