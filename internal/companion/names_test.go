package companion

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
	"wechat-bot/internal/identity"
)

type fixtureNames struct{ failed bool }

func (f fixtureNames) ResolveSpeaker(_ context.Context, room, sender string) (identity.Name, error) {
	if f.failed {
		return identity.Name{}, errors.New("directory unavailable")
	}
	return identity.Choose(sender, "同名备注", "群昵称", "昵称"), nil
}

func TestMemoryNamesPersistWithoutChangingIdentityContentOrClaims(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)
	if opened := store; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	c := enabledConfig()
	c.Permissions.Remember = true
	input := Input{Channel: "wechat", Account: "test", Thread: "one@chatroom", Sender: "peer-one", Text: "第一条", TraceID: "first", Mode: "direct"}
	now := time.Now()
	if err := store.Observe(input, c, now); err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimDecision(input, c, now); err != nil {
		t.Fatal(err)
	}
	other := input
	other.Thread = "other@chatroom"
	other.Text = "别的群"
	_ = store.Observe(other, c, now)
	model := &fakeModel{answer: `{"action":"reply","messages":["好的"]}`}
	engine, _ := New(c, store, model, "test")
	engine.Names = fixtureNames{}
	input.Sender = "peer-two"
	input.Text = "第二条"
	input.TraceID = "second"
	if _, err := engine.Respond(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	var history, current map[string]any
	_ = json.Unmarshal([]byte(model.turns[0]["content"]), &history)
	_ = json.Unmarshal([]byte(model.turns[len(model.turns)-1]["content"]), &current)
	if history["speaker"] != "同名备注" || current["speaker"] != "同名备注" || history["speaker_id"] != "peer-one" || current["speaker_id"] != "peer-two" {
		t.Fatal("same-name users merged", history, current)
	}
	reopened, err := NewStore(dir)
	if opened := reopened; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	if err != nil {
		t.Fatal(err)
	}
	scope, _ := reopened.Read(ScopeID("wechat", "test", "one@chatroom"), c, now)
	if len(scope.Messages) != 2 || scope.Messages[0].Text != "第一条" || scope.Messages[0].TraceID != "first" || scope.Messages[0].Sender != "peer-one" || scope.Messages[0].SenderName != "同名备注" || scope.LastDecision.IsZero() {
		t.Fatal("backfill lost identity, content or claim", scope)
	}
	otherScope, _ := reopened.Read(ScopeID("wechat", "test", "other@chatroom"), c, now)
	if otherScope.Messages[0].SenderName != "" {
		t.Fatal("backfill crossed conversations")
	}
	engine.Names = fixtureNames{failed: true}
	input.Sender = "peer-one"
	input.Text = "目录暂不可用"
	input.TraceID = "third"
	if _, err := engine.Respond(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(model.turns[len(model.turns)-1]["content"]), &current)
	if current["speaker"] != "同名备注" || current["speaker_id"] != "peer-one" {
		t.Fatal("temporary lookup failure discarded last name", current)
	}
}

func TestNamePriorityAndDisabledMemoryDoNotPersistMetadata(t *testing.T) {
	for _, tc := range []struct{ remark, room, nick, want, source string }{{"备注", "群昵称", "昵称", "备注", "remark"}, {"", "群昵称", "昵称", "群昵称", "room"}, {"", "", "昵称", "昵称", "nickname"}, {"", "", "", "peer", "id"}} {
		name := identity.Choose("peer", tc.remark, tc.room, tc.nick)
		if name.Text != tc.want || name.Source != tc.source {
			t.Fatal(name, tc)
		}
	}
	store, _ := NewStore(t.TempDir())
	if opened := store; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	c := enabledConfig()
	c.Permissions.Remember = false
	input := Input{Channel: "wechat", Account: "test", Thread: "friend", Sender: "peer", Text: "只临时理解", Mode: "direct"}
	model := &fakeModel{answer: `{"action":"silent","messages":[]}`}
	engine, _ := New(c, store, model, "test")
	engine.Names = fixtureNames{}
	if _, err := engine.Respond(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	scope, _ := store.Read(ScopeID("wechat", "test", "friend"), c, time.Now())
	if len(scope.Messages) != 0 {
		t.Fatal("disabled memory stored names or text")
	}
}
