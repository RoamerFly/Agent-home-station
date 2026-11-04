package companion

import (
	"context"
	"strings"
	"testing"
	"time"
)

func continuitySource(t *testing.T, s *Store, c Config, in Input, at time.Time) (string, MemorySource) {
	t.Helper()
	if err := s.Observe(in, c, at); err != nil {
		t.Fatal(err)
	}
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	id := eventID(scope, Turn{Role: "user", TraceID: in.TraceID}, 0)
	return scope, MemorySource{EventID: id, Quote: in.Text}
}

func TestDirectRecallIncludesCurrentContinuityWithoutLexicalOverlap(t *testing.T) {
	s, c, in := memoryFixture(t)
	engine, err := New(c, s, extractionFixture(func(context.Context, string, []map[string]string) (string, error) { return "", nil }), "")
	if err != nil {
		t.Fatal(err)
	}

	// The plan changes between turns. Neither "周日" nor "海边明信片" occurs
	// in the later generic question, so this verifies continuity recall rather
	// than a special phrase rule.
	in.TraceID, in.Text = "promise-saturday", "我们周六一起画一张海边明信片。"
	scope, first := continuitySource(t, s, c, in, time.Now().Add(-48*time.Hour))
	continuity, err := s.SaveCommitment(t.Context(), MemoryEntry{
		Scope: scope, Subject: in.Sender, Detail: "和 alice 约定周六一起画海边明信片。", Sources: []MemorySource{first}, CommitmentState: "open",
	})
	if err != nil {
		t.Fatal(err)
	}
	in.TraceID, in.Text = "promise-sunday", "改成周日，还是画海边明信片。"
	_, second := continuitySource(t, s, c, in, time.Now().Add(-24*time.Hour))
	continuity, err = s.SaveCommitment(t.Context(), MemoryEntry{
		Scope: scope, Subject: in.Sender, Detail: "和 alice 的约定已改为周日一起画海边明信片。", Sources: []MemorySource{second}, CommitmentState: "open", Corrects: continuity.ID, CorrectsVersion: continuity.Version,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Same sender/name on another platform has an independent continuity record.
	other := in
	other.Channel, other.Account, other.TraceID, other.Text = "other-platform", "other-account", "other-promise", "我们约在周一去山上看日出。"
	otherScope, otherSource := continuitySource(t, s, c, other, time.Now().Add(-time.Hour))
	if _, err = s.SaveCommitment(t.Context(), MemoryEntry{Scope: otherScope, Subject: other.Sender, Detail: "另一个平台的约定是周一看日出。", Sources: []MemorySource{otherSource}, CommitmentState: "open"}); err != nil {
		t.Fatal(err)
	}
	// A pinned preference is intentionally not a continuity anchor. It must not
	// ride along merely because generic follow-ups have no useful lexical terms.
	in.TraceID, in.Text = "unrelated-pinned", "我喜欢收集旧邮票。"
	_, unrelatedSource := continuitySource(t, s, c, in, time.Now().Add(-2*time.Hour))
	if _, err = s.SaveMemory(t.Context(), MemoryEntry{Scope: scope, Subject: in.Sender, Kind: "preference", Detail: "喜欢收集旧邮票。", Pinned: true, Sources: []MemorySource{unrelatedSource}}, 0); err != nil {
		t.Fatal(err)
	}

	in.TraceID, in.Text = "generic-follow-up", "我们约了什么？"
	packet := engine.memoryContext(t.Context(), in, Scope{})
	if !strings.Contains(packet, "周日一起画海边明信片") {
		t.Fatalf("current continuity missing from generic recall: %s", packet)
	}
	if strings.Contains(packet, "周六一起画海边明信片") || strings.Contains(packet, "另一个平台") || strings.Contains(packet, "旧邮票") {
		t.Fatalf("stale or cross-scope continuity leaked: %s", packet)
	}
	if !strings.Contains(packet, first.EventID) || !strings.Contains(packet, second.EventID) {
		t.Fatalf("continuity lost source evidence: %s", packet)
	}
}

func TestDirectRecallDoesNotBypassForgottenContinuity(t *testing.T) {
	s, c, in := memoryFixture(t)
	engine, err := New(c, s, extractionFixture(func(context.Context, string, []map[string]string) (string, error) { return "", nil }), "")
	if err != nil {
		t.Fatal(err)
	}
	in.TraceID, in.Text = "forgettable-promise", "我们约好下次一起做一张明信片。"
	scope, source := continuitySource(t, s, c, in, time.Now().Add(-time.Hour))
	continuity, err := s.SaveCommitment(t.Context(), MemoryEntry{Scope: scope, Subject: in.Sender, Detail: "和 alice 约好一起做明信片。", Sources: []MemorySource{source}, CommitmentState: "open"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("INSERT INTO memory_blocks(scope,event_id) VALUES(?,?)", scope, source.EventID); err != nil {
		t.Fatal(err)
	}
	in.TraceID, in.Text = "forgotten-follow-up", "我们约了什么？"
	packet := engine.memoryContext(t.Context(), in, Scope{})
	if strings.Contains(packet, "一起做明信片") || strings.Contains(packet, continuity.ID) {
		t.Fatalf("forgotten continuity returned: %s", packet)
	}
}

func TestDirectRecallLongOldEvidenceLeavesRoomForCurrentAgreement(t *testing.T) {
	s, c, in := memoryFixture(t)
	limits := MemoryDefaults()
	limits.ContinuityTokens = 320
	c.Memory = &limits
	engine, err := New(c, s, extractionFixture(nil), "")
	if err != nil {
		t.Fatal(err)
	}
	in.TraceID, in.Text = "long-original", "周六一起做明信片。"+strings.Repeat("蓝色海浪", 350)
	scope, first := continuitySource(t, s, c, in, time.Now().Add(-24*time.Hour))
	m, err := s.SaveCommitment(t.Context(), MemoryEntry{Scope: scope, Subject: in.Sender, Detail: "周六画明信片", Sources: []MemorySource{first}})
	if err != nil {
		t.Fatal(err)
	}
	in.TraceID, in.Text = "short-correction", "改到周日。"
	_, next := continuitySource(t, s, c, in, time.Now())
	_, err = s.SaveCommitment(t.Context(), MemoryEntry{Scope: scope, Subject: in.Sender, Detail: "周日画明信片", Corrects: m.ID, CorrectsVersion: m.Version, Sources: []MemorySource{next}})
	if err != nil {
		t.Fatal(err)
	}
	in.TraceID, in.Text = "next-question", "我们约了什么？"
	packet := engine.memoryContext(t.Context(), in, Scope{})
	if !strings.Contains(packet, m.ID) || !strings.Contains(packet, next.EventID) || !strings.Contains(packet, "周日画明信片") || strings.Contains(packet, "蓝色海浪") {
		t.Fatalf("oversized historical evidence displaced current agreement: %s", packet)
	}
}

func TestDirectRecallShowsClosedCommitmentStateOnlyWhenLexicallyRelevant(t *testing.T) {
	s, c, in := memoryFixture(t)
	engine, err := New(c, s, extractionFixture(func(context.Context, string, []map[string]string) (string, error) { return "", nil }), "")
	if err != nil {
		t.Fatal(err)
	}
	in.TraceID, in.Text = "promise-open", "我们约好一起做海边明信片。"
	scope, opened := continuitySource(t, s, c, in, time.Now().Add(-time.Hour))
	promise, err := s.SaveCommitment(t.Context(), MemoryEntry{Scope: scope, Subject: in.Sender, Detail: "和 alice 约好一起做海边明信片。", Sources: []MemorySource{opened}, CommitmentState: "open"})
	if err != nil {
		t.Fatal(err)
	}
	in.TraceID, in.Text = "promise-cancel", "海边明信片这次取消，不做了。"
	_, cancelled := continuitySource(t, s, c, in, time.Now())
	if _, err = s.SaveCommitment(t.Context(), MemoryEntry{Scope: scope, Subject: in.Sender, Detail: "和 alice 的海边明信片约定已经取消。", Sources: []MemorySource{cancelled}, CommitmentState: "cancelled", Corrects: promise.ID, CorrectsVersion: promise.Version}); err != nil {
		t.Fatal(err)
	}
	in.TraceID, in.Text = "closed-question", "海边明信片那个约定怎么样？"
	packet := engine.memoryContext(t.Context(), in, Scope{})
	if !strings.Contains(packet, "\"commitment_state\":\"cancelled\"") {
		t.Fatalf("closed commitment lacks explicit state: %s", packet)
	}
}
