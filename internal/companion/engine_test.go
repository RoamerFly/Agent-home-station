package companion

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestImageSelectionRespectsGlobalSwitchAndKnownAssets(t *testing.T) {
	c := enabledConfig()
	c.Permissions.SendImage = false
	model := &fakeModel{answer: `{"action":"reply","messages":[],"image_id":"approved-image"}`}
	engine, err := NewPreview(c, model, "global")
	if err != nil {
		t.Fatal(err)
	}
	engine.Images = []ImageAsset{{ID: "approved-image", Title: "鲸鱼娘", Description: "开心地挥手"}}
	input := Input{Mode: "direct", Text: "发张图片", Preview: true}
	decision, err := engine.Respond(context.Background(), input)
	if err != nil || decision.Action != "blocked" {
		t.Fatal("global image permission ignored", decision, err)
	}
	engine.Config.Permissions.SendImage = true
	engine.Config.Permissions.ReplyText = false
	decision, err = engine.Respond(context.Background(), input)
	if err != nil || decision.Action != "reply" || decision.ImageID != "approved-image" || len(decision.Messages) != 0 {
		t.Fatal("image-only response failed", decision, err)
	}
	model.answer = `{"action":"reply","messages":[],"image_id":"unapproved-image"}`
	decision, err = engine.Respond(context.Background(), input)
	if err != nil || decision.Action != "blocked" {
		t.Fatal("unapproved asset accepted", decision, err)
	}
}

type fakeModel struct {
	calls  int
	answer string
	system string
	turns  []map[string]string
}

func (m *fakeModel) Chat(_ context.Context, system string, turns []map[string]string) (string, error) {
	m.calls++
	m.system = system
	m.turns = turns
	if system == groundingCheckPolicy {
		return syntheticGroundingVerdict(turns, "allow"), nil
	}
	return m.answer, nil
}
func enabledConfig() Config {
	c := Defaults()
	// Existing model-review tests opt in; production defaults stay off.
	c.Grounding = &GroundingSettings{Enabled: true}
	c.Enabled = true
	c.QuietStart = "00:00"
	c.QuietEnd = "00:00"
	return c
}

// Model/tool ordering tests still use a real inbound identity when exercising
// non-preview delivery. No fake source is injected by production code.
func syntheticNativeInput(in Input, id string) Input {
	if in.Sender == "" {
		in.Sender = "synthetic-sender"
	}
	in.TraceID = id
	in.MessageTime = time.Now().UTC().Format(time.RFC3339Nano)
	words := UserRequestText(in.Text)
	in.OriginalText = &words
	in.Source = &SourceRef{InstanceID: "synthetic-test-adapter", MessageID: id, ServerID: "server-" + id, Account: in.Account, Thread: in.Thread, Sender: in.Sender, MessageType: 1, OccurredAt: in.MessageTime, BodyHash: sourceHash(words), Revision: 1, Integrity: "verified"}
	return in
}
func TestPermissionsSilenceAndInvalidOutput(t *testing.T) {
	c := enabledConfig()
	store, err := NewStore(t.TempDir())
	if opened := store; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	if err != nil {
		t.Fatal(err)
	}
	model := &fakeModel{answer: `{"action":"reply","messages":["你好呀"]}`}
	engine, err := New(c, store, model, "global")
	if err != nil {
		t.Fatal(err)
	}
	input := Input{Channel: "wechat", Account: "one", Thread: "room@chatroom", Sender: "synthetic-member", TraceID: "permission-test-message", Text: "hi", Mode: "social", IsGroup: true}
	input.Source = &SourceRef{MessageID: input.TraceID, ServerID: "permission-test-server", Account: input.Account, Thread: input.Thread, Sender: input.Sender, Integrity: "verified", BodyHash: "synthetic-hi-body-hash", OccurredAt: time.Now().Format(time.RFC3339Nano)}
	d, err := engine.Respond(context.Background(), input)
	if err != nil || d.Action != "blocked" || model.calls != 0 {
		t.Fatal("unauthorized observation invoked model", d, err)
	}
	input.Mode = "direct"
	d, err = engine.Respond(context.Background(), input)
	if err != nil || d.Action != "reply" || model.calls != 1 {
		t.Fatal(d, err)
	}
	scope := store.Context(input, c, time.Now())
	if len(scope.Messages) != 0 {
		t.Fatal("memory recorded without permission")
	}
	engine.Config.Permissions.ReplyText = false
	engine.Config.Permissions.SendImage = false
	engine.Config.Permissions.SendVoice = false
	engine.Config.Permissions.SendVideo = false
	engine.Config.Permissions.SendFile = false
	d, _ = engine.Respond(context.Background(), input)
	if d.Action != "blocked" || model.calls != 1 {
		t.Fatal("text permission ignored")
	}
	engine.Config.Permissions.ReplyText = true
	for _, answer := range []string{`{"action":"silent","messages":["should not send"]}`, `{"action":"reply","messages":[""]}`} {
		model.answer = answer
		d, err = engine.Respond(context.Background(), input)
		if err != nil || d.Action != "silent" || len(d.Messages) != 0 {
			t.Fatal("invalid/silent answer allowed messages", d, err)
		}
	}
}
func TestMemoryIsolationPersistenceAndClearPreservesClaims(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if opened := store; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	if err != nil {
		t.Fatal(err)
	}
	c := enabledConfig()
	c.Permissions.Remember = true
	now := time.Now()
	input := Input{Channel: "wechat", Account: "one", Thread: "filehelper", Text: "private-context", Sender: "alice", Mode: "direct"}
	if err = store.Observe(input, c, now); err != nil {
		t.Fatal(err)
	}
	if err = store.Accepted(input, "answer", c, now); err != nil {
		t.Fatal(err)
	}
	for _, other := range []Input{
		{Channel: "wechat", Account: "two", Thread: input.Thread},
		{Channel: "qq", Account: input.Account, Thread: input.Thread},
		{Channel: input.Channel, Account: input.Account, Thread: "other"},
	} {
		if len(store.Context(other, c, now).Messages) != 0 {
			t.Fatal("memory crossed identity boundary")
		}
	}
	c.Permissions.Remember = false
	if len(store.Context(input, c, now).Messages) != 0 {
		t.Fatal("disabled memory still used")
	}
	c.Permissions.Remember = true
	c.Permissions.Proactive = true
	c.ProactiveThreads = []string{input.Thread}
	now = now.Add(2 * time.Hour)
	if err = store.ClaimProactive(input, c, now); err != nil {
		t.Fatal(err)
	}
	id := ScopeID(input.Channel, input.Account, input.Thread)
	if err = store.Note(id, "relationship-note"); err != nil {
		t.Fatal(err)
	}
	if err = store.Clear(id); err != nil {
		t.Fatal(err)
	}
	store, err = NewStore(dir)
	if opened := store; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	if err != nil {
		t.Fatal(err)
	}
	scope, err := store.Read(id, c, now)
	if err != nil || len(scope.Messages) != 0 || len(scope.Notes) != 0 || scope.LastProactive.IsZero() {
		t.Fatal("clear reset claims or retained content", scope, err)
	}
	if err = store.ClaimProactive(input, c, now); err == nil {
		t.Fatal("restart/clear allowed duplicate proactive opportunity")
	}
	info, err := os.Stat(filepath.Join(dir, "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_ = info // Linux service and temporary files enforce private permissions.
}
func TestPreviewUsesNoRealMemoryOrBudget(t *testing.T) {
	c := enabledConfig()
	c.Permissions.Remember = true
	model := &fakeModel{answer: `{"action":"reply","messages":["预演成功"]}`}
	engine, err := NewPreview(c, model, "trusted-global")
	if err != nil {
		t.Fatal(err)
	}
	input := Input{Mode: "direct", Text: "hypothetical", Preview: true}
	d, err := engine.Respond(context.Background(), input)
	if err != nil || d.Action != "reply" || model.calls != 1 {
		t.Fatal(d, err)
	}
	if len(engine.Store.Summaries(c, time.Now())) != 0 {
		t.Fatal("preview persisted conversation")
	}
	a, b := engine.Store.Counters(c, time.Now())
	if a != 0 || b != 0 || len(model.turns) != 1 {
		t.Fatal("preview used real memory/budget")
	}
	if !strings.Contains(model.system, c.Name) || !strings.Contains(model.system, "trusted-global") {
		t.Fatal("persona/global policy missing")
	}
}

func TestCurrentMessageIsDistinctFromStoredIdentityQuestions(t *testing.T) {
	c := enabledConfig()
	c.Permissions.Remember = true
	store, err := NewStore(t.TempDir())
	if opened := store; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	if err != nil {
		t.Fatal(err)
	}
	input := Input{Channel: "wechat", Account: "test", Thread: "room@chatroom", Sender: "friend", Text: "你是谁", TraceID: "historical-question", Source: &SourceRef{InstanceID: "identity-questions", MessageID: "historical-question", Account: "test", Thread: "room@chatroom", Sender: "friend", Integrity: "verified"}, Mode: "direct", Directed: true, IsGroup: true}
	if err := store.Observe(input, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.Accepted(input, "我是蓝色大肥鱼。", c, time.Now()); err != nil {
		t.Fatal(err)
	}
	model := &firstContextModel{fakeModel: fakeModel{answer: `{"action":"reply","messages":["我刚刚说偏了，先回答你现在的问题。"]}`}}
	engine, err := New(c, store, model, "global")
	if err != nil {
		t.Fatal(err)
	}
	input.Text = "什么意思"
	input.TraceID = "current-question"
	input.Source = &SourceRef{InstanceID: "identity-questions", MessageID: "current-question", Account: "test", Thread: "room@chatroom", Sender: "friend", Integrity: "verified"}
	decision, err := engine.Respond(context.Background(), input)
	if err != nil || decision.Action != "reply" || len(model.firstTurns) != 3 {
		t.Fatal("conversation context missing", decision, err)
	}
	var historical, current map[string]any
	if json.Unmarshal([]byte(model.firstTurns[0]["content"]), &historical) != nil || json.Unmarshal([]byte(model.firstTurns[2]["content"]), &current) != nil {
		t.Fatal("invalid context envelope")
	}
	if historical["kind"] != "history_message" || historical["text"] != "你是谁" || current["kind"] != "current_message" || current["text"] != "什么意思" {
		t.Fatal("current target mixed with old identity question")
	}
	stored := store.Context(input, c, time.Now())
	if len(stored.Messages) != 3 || stored.Messages[0].Text != "你是谁" || stored.Messages[1].Text != "我是蓝色大肥鱼。" {
		t.Fatal("fix rewrote stored conversation")
	}
	// A direct identity question may still produce an introduction; there is no name-stripping filter.
	model.answer = `{"action":"reply","messages":["我叫蓝色大肥鱼。"]}`
	input.Text = "你叫什么名字？"
	input.TraceID = "next-identity-question"
	input.Source = &SourceRef{InstanceID: "identity-questions", MessageID: input.TraceID, Account: "test", Thread: input.Thread, Sender: input.Sender, Integrity: "verified"}
	decision, err = engine.Respond(context.Background(), input)
	if err != nil || decision.Messages[0] != "我叫蓝色大肥鱼。" {
		t.Fatal("legitimate identity reply was removed")
	}
}
func TestExpiryQuietHoursAndPersistentBudgets(t *testing.T) {
	c := enabledConfig()
	c.Permissions.Remember = true
	c.MaxRepliesPerHour = 1
	c.MemoryDays = 7
	dir := t.TempDir()
	store, _ := NewStore(dir)
	if opened := store; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	now := time.Now()
	input := Input{Channel: "wechat", Account: "one", Thread: "room", Text: "expired"}
	if err := store.Observe(input, c, now.Add(-8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.Expire(c, now); err != nil {
		t.Fatal(err)
	}
	if len(store.Context(input, c, now).Messages) != 0 {
		t.Fatal("expired memory used")
	}
	if err := store.ClaimReply(c, now); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(dir)
	if opened := store; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ClaimReply(c, now); err != nil {
		t.Fatal("legacy budget blocked reply after restart", err)
	}
	if replies, _ := store.Counters(c, now); replies != 2 {
		t.Fatal("diagnostic count lost", replies)
	}
	if err = store.ClaimReply(c, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	c.QuietStart = "23:00"
	c.QuietEnd = "08:00"
	night := time.Date(2026, 10, 24, 0, 30, 0, 0, location(c.Timezone))
	day := time.Date(2026, 10, 24, 12, 0, 0, 0, location(c.Timezone))
	if !c.Quiet(night) || c.Quiet(day) {
		t.Fatal("overnight quiet interval wrong")
	}
}

func TestPlainReplyAndRequiredMentionRespectPermissions(t *testing.T) {
	c := enabledConfig()
	model := &fakeModel{answer: "我是鲸鱼娘，看到你的消息啦。"}
	e, err := NewPreview(c, model, "global")
	if err != nil {
		t.Fatal(err)
	}
	input := Input{Mode: "direct", Preview: true, Text: "你是谁"}
	decision, err := e.Respond(context.Background(), input)
	if err != nil || decision.Action != "reply" || decision.Messages[0] != model.answer {
		t.Fatal("natural reply dropped", decision, err)
	}
	model.answer = `{"action":"silent","messages":[],"reason":"silent"}`
	input.MustReply = true
	decision, err = e.Respond(context.Background(), input)
	if err != nil || decision.Action != "silent" || len(decision.Messages) != 0 {
		t.Fatal("program replaced model silence with a fixed mention reply", decision, err)
	}
	e.Config.Permissions.ReplyText = false
	decision, _ = e.Respond(context.Background(), input)
	if decision.Action != "silent" {
		t.Fatal("forced mention bypassed text permission", decision)
	}
	e.Config.Permissions.ReplyText = true
	model.answer = `{"bad":`
	decision, _ = e.Respond(context.Background(), input)
	if decision.Reason != "invalid_model_decision" {
		t.Fatal("parse error mislabeled as silence", decision)
	}
}

// A risk review may make another model call. Check the initial request envelope
// independently of the later narrow review payload.
type firstContextModel struct {
	fakeModel
	firstTurns []map[string]string
}

func (m *firstContextModel) Chat(ctx context.Context, system string, turns []map[string]string) (string, error) {
	if m.firstTurns == nil {
		m.firstTurns = append([]map[string]string(nil), turns...)
	}
	return m.fakeModel.Chat(ctx, system, turns)
}
