package companion

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func TestQuoteRoleGraphDoesNotTurnOriginalAuthorIntoQuoter(t *testing.T) {
	s, c, _ := memoryFixture(t)
	old := groundedInput(t, s, c, "member-alpha", "quote-original", "周末想爬山。")
	ref, ok := s.SourceForInput(old)
	if !ok {
		t.Fatal("missing original")
	}
	current := groundedInput(t, s, c, "member-beta", "quote-current", "我引用这条资料，你核对一下。")
	current.Source, _ = s.SourceForInput(current)
	current.QuotedSource = ref
	words := current.Text
	current.OriginalText = &words
	if err := s.Observe(current, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s, Config: c, model: &groundingSequenceModel{}}
	d, err := e.normalizeReplyContexts(t.Context(), current, Decision{Action: "reply", Messages: []string{"member-alpha引用的这条说周末想爬山。"}})
	if err != nil {
		t.Fatal(err)
	}
	graph := e.quoteRoleEvidence(t.Context(), current, d)
	if len(graph.Edges) != 1 || graph.Edges[0].QuotedByID != "member-beta" || graph.Edges[0].QuotedAuthorID != "member-alpha" {
		t.Fatal("quote direction lost", graph)
	}
	if !contradictsQuoteRole(graph, d.Messages) {
		t.Fatal("original author became quoter", graph)
	}
	for _, good := range []string{"member-beta引用了member-alpha的原话，是member-alpha说周末想爬山。", "不是member-alpha引用的，是member-beta引用的。", "原文是member-alpha写的。"} {
		if contradictsQuoteRole(graph, []string{good}) {
			t.Fatal("correct role distinction rejected", good)
		}
	}
	verdict, err := e.checkReplyGrounding(t.Context(), current, d)
	if err != nil || verdict.Verdict != "reject" {
		t.Fatal("model allow bypassed explicit quote graph", verdict, err)
	}
}

func TestCandidateIdentityIsBoundByHostNotModelEcho(t *testing.T) {
	s, c, _ := memoryFixture(t)
	in := groundedInput(t, s, c, "alpha", "host-current", "明确原话。")
	model := &groundingSequenceModel{}
	e := &Engine{Store: s, Config: c, model: model}
	verdict, err := e.checkReplyGrounding(t.Context(), in, Decision{Action: "reply", Messages: []string{"明确原话。"}})
	if err != nil {
		t.Fatal(err)
	}
	state := model.inputs[0]
	if state["candidate_id"] != nil || state["candidate_sha256"] != nil || len(verdict.CandidateID) != 64 {
		t.Fatal("identity echo leaked into model protocol", state)
	}
	raw, _ := canonicalGroundingJSON(verdict.ReviewBinding)
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != verdict.CandidateID {
		t.Fatal("host digest does not bind canonical snapshot")
	}
	answer := `{"candidate_id":"untrusted","bubbles":[{"index":0,"verdict":"allow","violations":[]}]}`
	if _, err = decodeGroundingVerdict(answer, verdict.CandidateID, []string{"明确原话。"}, nil); err == nil {
		t.Fatal("model supplied host identity")
	}
}
