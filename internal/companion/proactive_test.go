package companion

import (
	"strings"
	"testing"
	"time"
)

func TestProactiveChoiceUsesConversationWithoutPriorSilenceTally(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	c := enabledConfig()
	c.Permissions.Proactive, c.Permissions.Remember = true, true
	in := Input{Channel: "wechat", Account: "fixture", Thread: "room@chatroom", Sender: "member", IsGroup: true, Mode: "direct", Text: "周末的观星活动在周六"}
	if err := s.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	in.Mode, in.Sender, in.Text = "proactive", "", "一次获准的自主评估"
	m := &fakeModel{}
	e, err := New(c, s, m, "test")
	if err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct{ answer, reason string }{
		{`{"action":"silent","reason":"awaiting_participant","messages":[]}`, "awaiting_participant"},
		{`{"action":"silent","reason":"arbitrary text","messages":[]}`, "model_chose_silence"},
		{`{"action":"reply","messages":["周六的观星打算看哪个星座？"]}`, "model_chose_reply"},
	} {
		in.TraceID = []string{"proactive-first", "proactive-second", "proactive-third"}[i]
		m.answer = tc.answer
		d, err := e.Respond(t.Context(), in)
		if err != nil || d.Reason != tc.reason || m.calls != i+1 {
			t.Fatal(i, d, err, m.calls)
		}
		for _, turn := range m.turns {
			if strings.Contains(turn["content"], "autonomous_state") || strings.Contains(turn["content"], "last_outcome_trace") {
				t.Fatal("administrative outcomes leaked into decision")
			}
		}
		if err := s.ProactiveOutcome(in, d.Action, d.Reason, c, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProactiveTargetsIsolationTimingBudgetsAndRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if opened := s; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	if err != nil {
		t.Fatal(err)
	}
	c := enabledConfig()
	c.Permissions.Proactive = true
	c.Permissions.Remember = true
	c.ProactiveMaxPerDay = 5
	c.ProactiveThreads = []string{"room@chatroom", "friend"}
	c.ProactiveRules = []ProactiveRule{{Thread: "room@chatroom", IdleMinutes: 30, MaxPerDay: 2}, {Thread: "friend", IdleMinutes: 90, MaxPerDay: 1}}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.FixedZone("CST", 8*3600))
	input := func(thread, account string) Input {
		return Input{Channel: "wechat", Account: account, Thread: thread, Text: "可用上下文", Sender: "peer", Mode: "direct"}
	}
	for _, thread := range []string{"room@chatroom", "friend", "not-selected"} {
		if err = s.Observe(input(thread, "account"), c, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	candidates := s.ProactiveCandidates("wechat", "account", c, now)
	if len(candidates) != 1 || candidates[0].Thread != "room@chatroom" || !candidates[0].IsGroup {
		t.Fatal("per-target timing not applied", candidates)
	}
	if len(s.ProactiveCandidates("wechat", "other", c, now)) != 0 || len(s.ProactiveCandidates("qq", "account", c, now)) != 0 {
		t.Fatal("identity crossed")
	}
	if err = s.ClaimProactive(input("not-selected", "account"), c, now); err == nil {
		t.Fatal("unselected target claimed")
	}
	if err = s.ClaimProactive(candidates[0], c, now); err != nil {
		t.Fatal(err)
	}
	if err = s.ClaimProactive(candidates[0], c, now); err == nil {
		t.Fatal("immediate duplicate claim")
	}
	s, err = NewStore(dir)
	if opened := s; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	if err != nil {
		t.Fatal(err)
	}
	state := s.ProactiveState("wechat", "account", "room@chatroom", c, now)
	if state.Today != 1 || state.Reason != "scheduled_wait" || state.NextAt.Before(now.Add(15*time.Minute)) || !state.NextAt.Before(now.Add(45*time.Minute)) {
		t.Fatal(state)
	}
	for i := 0; i < 8; i++ {
		now = state.NextAt
		_ = s.TouchActivity(candidates[0], now)
		if err = s.ClaimProactive(candidates[0], c, now); err != nil {
			t.Fatal("legacy quota blocked evaluation", i, err)
		}
		state = s.ProactiveState("wechat", "account", "room@chatroom", c, now)
		if state.Reason != "scheduled_wait" || state.Today != i+2 {
			t.Fatal(state)
		}
	}

}

func TestProactiveLegacyLedgerAndActivityRemainPrivate(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	if opened := s; opened != nil {
		t.Cleanup(func() { _ = opened.Close() })
	}
	c := enabledConfig()
	c.Permissions.Remember = true
	c.Permissions.Proactive = true
	c.ProactiveThreads = []string{"friend"}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	in := Input{Channel: "wechat", Account: "a", Thread: "friend", Text: "remembered"}
	_ = s.Observe(in, c, now.Add(-2*time.Hour))
	id := ScopeID(in.Channel, in.Account, in.Thread)
	_ = s.transaction(func(data *persisted) error {
		v := data.Scopes[id]
		v.LastProactive = now.Add(-time.Hour)
		data.Scopes[id] = v
		return nil
	})
	if state := s.ProactiveState(in.Channel, in.Account, in.Thread, c, now); state.Today != 1 || state.Reason != "ready" {
		t.Fatal("legacy quota still enforced", state)
	}
	c.ProactiveRules = []ProactiveRule{{Thread: "friend", IdleMinutes: 5, MaxPerDay: 2}}
	if err := s.TouchActivity(in, now); err != nil {
		t.Fatal(err)
	}
	if state := s.ProactiveState(in.Channel, in.Account, in.Thread, c, now); state.Reason != "ready" {
		t.Fatal("ambient activity still blocks autonomous evaluation", state)
	}
	scope := s.Context(in, c, now)
	if len(scope.Messages) != 1 || scope.Messages[0].Text != "remembered" {
		t.Fatal("activity memorized or changed body", scope)
	}
	_ = s.TouchActivity(in, now.Add(-time.Hour))
	if state := s.ProactiveState(in.Channel, in.Account, in.Thread, c, now.Add(5*time.Minute)); state.Reason != "ready" {
		t.Fatal(state)
	}
	for _, test := range []struct {
		name, want string
		change     func(*Config)
	}{
		{"disabled", "character_disabled", func(c *Config) { c.Enabled = false }},
		{"permission", "proactive_permission_denied", func(c *Config) { c.Permissions.Proactive = false }},
		{"memory", "no_reliable_context", func(c *Config) { c.Permissions.Remember = false }},
		{"quiet", "quiet_hours", func(c *Config) { c.QuietStart = "00:00"; c.QuietEnd = "23:59" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			copy := c
			test.change(&copy)
			if st := s.ProactiveState(in.Channel, in.Account, in.Thread, copy, now); st.Reason != test.want {
				t.Fatal(st)
			}
			if s.ClaimProactive(in, copy, now) == nil {
				t.Fatal("blocked claim succeeded")
			}
		})
	}
	_ = s.Clear(id)
	if state := s.ProactiveState(in.Channel, in.Account, in.Thread, c, now.Add(6*time.Minute)); state.Today != 1 || state.Reason != "no_reliable_context" {
		t.Fatal("clear reset claim", state)
	}
}

func TestProactiveRuleValidation(t *testing.T) {
	c := Defaults()
	c.ProactiveThreads = []string{"friend"}
	c.ProactiveRules = []ProactiveRule{{Thread: "friend", IdleMinutes: 5, MaxPerDay: 2}}
	if err := Validate(c); err != nil {
		t.Fatal(err)
	}
	for _, rule := range []ProactiveRule{{Thread: "other", IdleMinutes: 5, MaxPerDay: 2}, {Thread: "friend", IdleMinutes: 0, MaxPerDay: 1}, {Thread: "friend", IdleMinutes: 5, MaxPerDay: -1}} {
		copy := c
		copy.ProactiveRules = []ProactiveRule{rule}
		if Validate(copy) == nil {
			t.Fatal("invalid rule accepted", rule)
		}
	}
	c.ProactiveRules = append(c.ProactiveRules, c.ProactiveRules[0])
	if Validate(c) == nil {
		t.Fatal("duplicate override accepted")
	}
}

func TestQuietChoicesAndNonSendingFailuresHaveDistinctStatuses(t *testing.T) {
	for code, expected := range map[string]string{
		"topic_concluded": "silent", "model_chose_silence": "silent",
		"tool_budget_exhausted": "failed", "unverified_model_response": "failed",
		"quiet_hours": "skipped", "no_reliable_context": "skipped",
		"cross_conversation_private_route_unavailable":       "blocked",
		"cross_conversation_result_private_delivery_handled": "completed",
	} {
		d := Decision{Action: "silent", Reason: code}
		if got := d.RecordStatus(); got != expected {
			t.Errorf("%s: got %s, want %s", code, got, expected)
		}
	}
}
