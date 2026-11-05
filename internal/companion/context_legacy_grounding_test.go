package companion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLegacyModelRefreshPreservesPersonalMediaRedaction(t *testing.T) {
	s, c, in := memoryFixture(t)
	in.Sender = "other"
	in.TraceID = "other-image"
	in.Text = "视觉观察：private lunch on keyboard"
	in.IncomingMedia = []IncomingMediaRef{{UploaderID: in.Sender, Kind: "image", Observation: in.Text}}
	if err := s.Observe(in, c, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.Accepted(in, "private lunch reply", c, time.Now()); err != nil {
		t.Fatal(err)
	}
	current := in
	current.Sender = "current"
	current.TraceID = "now"
	current.IsGroup = true
	current.IncomingMedia = nil
	current.Text = "晚安"
	model := &firstContextModel{fakeModel: fakeModel{answer: `{"action":"silent","messages":[]}`}}
	e, err := New(c, s, model, "base")
	if err != nil {
		t.Fatal(err)
	}
	turns := e.contextTurns(s.Context(current, c, time.Now()), current)
	turns = e.refreshMemoryPackets(t.Context(), turns)
	if _, err := model.Chat(t.Context(), "base", turns); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(model.firstTurns)
	if strings.Contains(string(raw), "private lunch") || strings.Contains(string(raw), "keyboard") {
		t.Fatal("legacy refresh restored private image/reply", string(raw))
	}
	if !strings.Contains(string(raw), "personal_context_redacted") {
		t.Fatal("missing redaction evidence", string(raw))
	}
}
func TestLegacyModelSummaryInvalidatedBeforeNextCall(t *testing.T) {
	s, c, in := memoryFixture(t)
	in.Text = "private old preference"
	m := sourceEntry(t, s, c, in, time.Now(), "preference", in.Text, 0)
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	summary := contextSummary{Version: 1, People: []summaryPerson{}, Topics: []summaryTopic{{Text: in.Text, SourceEventIDs: []string{m.Sources[0].EventID}}}, Media: []summaryMedia{}, PendingTasks: []summaryTask{}, Preferences: []string{}, Corrections: []string{}, Sources: []SourceRef{{EventID: m.Sources[0].EventID, Revision: 1, Sender: in.Sender}}}
	raw, _ := json.Marshal(summary)
	if err := s.scopeTransaction(scope, func(p *persisted) error {
		x := p.Scopes[scope]
		x.Summary = string(raw)
		p.Scopes[scope] = x
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	model := &firstContextModel{fakeModel: fakeModel{answer: `{"action":"silent","messages":[]}`}}
	e, err := New(c, s, model, "base")
	if err != nil {
		t.Fatal(err)
	}
	current := in
	current.TraceID = "now"
	turns := e.contextTurns(s.Context(current, c, time.Now()), current)
	before, _ := json.Marshal(turns)
	if !strings.Contains(string(before), "conversation_summary") {
		t.Fatal("summary fixture missing")
	}
	m.Status = "deleted"
	if _, err := s.SaveMemory(t.Context(), m, m.Version); err != nil {
		t.Fatal(err)
	}
	turns = e.refreshMemoryPackets(context.Background(), turns)
	if _, err := model.Chat(t.Context(), "base", turns); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(model.firstTurns)
	if strings.Contains(string(after), in.Text) || strings.Contains(string(after), "conversation_summary") {
		t.Fatal("forgotten legacy summary reached model", string(after))
	}
}
