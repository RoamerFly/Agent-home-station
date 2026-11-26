package companion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type socialGateModel struct {
	preflightAnswer string
	preflightErr    error
	preflightCalls  int
	fullCalls       int
	preflightInput  map[string]any
}

func (*socialGateModel) SupportsTaskReasoning() bool { return true }

func (m *socialGateModel) Chat(_ context.Context, system string, turns []map[string]string) (string, error) {
	if system == socialPreflightPolicy {
		m.preflightCalls++
		if len(turns) != 1 || turns[0]["role"] != "user" {
			return "", errors.New("preflight used full conversation")
		}
		if err := json.Unmarshal([]byte(turns[0]["content"]), &m.preflightInput); err != nil {
			return "", err
		}
		return m.preflightAnswer, m.preflightErr
	}
	m.fullCalls++
	return `{"action":"silent","messages":[]}`, nil
}

func socialGateFixture(t *testing.T, m *socialGateModel) (*Engine, *Store, Config) {
	t.Helper()
	c := enabledConfig()
	c.Permissions.ObserveGroups = true
	c.Permissions.AutonomousGroups = true
	c.Permissions.Remember = true
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	e, err := New(c, store, m, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	return e, store, c
}

func socialGateInput(text, trace string) Input {
	return Input{Channel: "wechat", Account: "synthetic", Thread: "room@chatroom", Sender: "member", Text: text, TraceID: trace, Mode: "social", IsGroup: true}
}

func TestSocialPreflightShortAcknowledgementPersistsSilentDecision(t *testing.T) {
	m := &socialGateModel{preflightAnswer: `{"action":"silent"}`}
	e, store, c := socialGateFixture(t, m)
	var events []string
	e.ToolLog = func(event string) { events = append(events, event) }
	in := socialGateInput("好", "short-ack")
	d, err := e.Respond(t.Context(), in)
	if err != nil || d.Action != "silent" || d.Reason != "social_attention_silent" || m.preflightCalls != 1 || m.fullCalls != 0 {
		t.Fatal(d, err, m.preflightCalls, m.fullCalls)
	}
	if len(events) != 1 || events[0] != "social_preflight_silent" {
		t.Fatal("compact judgement was not counted", events)
	}
	scope := store.Context(in, c, time.Now())
	if scope.LastDecision.IsZero() || len(scope.Messages) != 1 || scope.Messages[0].Text != "好" {
		t.Fatal("silent decision lost observed message or decision time", scope)
	}
}

func TestSocialPreflightUsesGroupPreferenceAndShareBypasses(t *testing.T) {
	m := &socialGateModel{preflightAnswer: `{"action":"continue"}`}
	e, _, _ := socialGateFixture(t, m)
	pref := "本群收到视频号等分享就总结；其他普通聊天由你决定是否沉默。"
	e.ConversationPreference = func(Input) (string, bool) { return pref, true }
	d, err := e.Respond(t.Context(), socialGateInput("好", "pref-ack"))
	if err != nil || d.Action != "silent" || m.preflightCalls != 1 || m.fullCalls != 1 || m.preflightInput["conversation_preferences"] != pref {
		t.Fatal("preference-first preflight did not fall through to the full agent", d, err, m)
	}
	share := socialGateInput("分享一个视频", "share-1")
	share.GroupShareSummary = true
	_, err = e.Respond(t.Context(), share)
	if err != nil || m.preflightCalls != 1 || m.fullCalls != 2 {
		t.Fatal("host share summary was intercepted by the social preflight", err, m)
	}
}

func TestSocialPreflightDoesNotInterceptAnswerToOwnQuestion(t *testing.T) {
	m := &socialGateModel{preflightAnswer: `{"action":"silent"}`}
	e, store, c := socialGateFixture(t, m)
	seed := socialGateInput("先聊聊歌", "seed")
	if err := store.Observe(seed, c, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.Accepted(seed, "你想让我唱哪首？", c, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	d, err := e.Respond(t.Context(), socialGateInput("好，就那首", "answer"))
	if err != nil || d.Reason != "model_chose_silence" || m.preflightCalls != 0 || m.fullCalls != 1 {
		t.Fatal("answer to an open question was preflighted", d, err, m)
	}
}

func TestSocialPreflightLeavesRequestsAndMediaToFullAgent(t *testing.T) {
	m := &socialGateModel{preflightAnswer: `{"action":"silent"}`}
	e, _, _ := socialGateFixture(t, m)
	requests := []Input{
		socialGateInput("帮我唱一首歌", "request"),
		socialGateInput("这张图是什么", "question"),
	}
	media := socialGateInput("这张挺好", "media")
	media.IncomingMedia = []IncomingMediaRef{{AssetID: "synthetic-image", Kind: "image"}}
	requests = append(requests, media)
	for _, in := range requests {
		if _, err := e.Respond(t.Context(), in); err != nil {
			t.Fatal(in.Text, err)
		}
	}
	if m.preflightCalls != 0 || m.fullCalls != len(requests) {
		t.Fatal("direct request or media was intercepted", m.preflightCalls, m.fullCalls)
	}
}

func TestSocialPreflightFailureOrInvalidFormatFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer string
		err    error
	}{
		{name: "model error", err: errors.New("synthetic provider error")},
		{name: "malformed output", answer: "maybe"},
		{name: "contradictory output", answer: `{"action":"silent","messages":["hello"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &socialGateModel{preflightAnswer: tc.answer, preflightErr: tc.err}
			e, _, _ := socialGateFixture(t, m)
			var event string
			e.ToolLog = func(v string) { event = v }
			d, err := e.Respond(t.Context(), socialGateInput("嗯", "fallback"))
			if err != nil || d.Reason != "model_chose_silence" || m.preflightCalls != 1 || m.fullCalls != 1 || event != "social_preflight_fallback" {
				t.Fatal("full agent fallback was lost", d, err, m, event)
			}
		})
	}
	if strings.Contains(socialPreflightPolicy, "【可自主使用的工具】") {
		t.Fatal("preflight must not request the full tool catalog")
	}
}
