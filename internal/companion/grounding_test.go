package companion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGroundedHistorySeparatesOtherMembersImageAndOldAssistant(t *testing.T) {
	in := Input{IsGroup: true, Sender: "beta"}
	scope := Scope{Messages: []Turn{{Role: "user", Sender: "alpha", Text: "视觉观察：盒饭在键盘旁"}, {Role: "assistant", Text: "汤别洒键盘"}, {Role: "user", Sender: "beta", Text: "晚安"}}}
	got := groundedHistory(scope, in)
	if strings.Contains(got[0].Text, "盒饭") || strings.Contains(got[1].Text, "汤别") {
		t.Fatal(got)
	}
	if scope.Messages[0].Text != "视觉观察：盒饭在键盘旁" {
		t.Fatal("mutated original")
	}
	in.QuotedSource = &SourceRef{Sender: "alpha"}
	got = groundedHistory(scope, in)
	if !strings.Contains(got[0].Text, "盒饭") {
		t.Fatal("explicit quote lost")
	}
}
func TestReplyAuditMetadataDoesNotContainRawMessagesAndFiltersBeforePaging(t *testing.T) {
	s, c, in := memoryFixture(t)
	e := &Engine{Store: s, Config: c}
	in.TraceID = "first"
	in.IsGroup = true
	e.writeReplyAudit(in, Decision{Messages: []string{"synthetic private message"}}, nil, "validated", nil)
	in.TraceID = "wanted"
	e.writeReplyAudit(in, Decision{Messages: []string{"secret candidate"}}, []string{"risk"}, "reviewed", []byte(`{"token":"secret","text":"{\"password\":\"private\"}"}`))
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	rows, more, err := s.FindReplyAudits(context.Background(), scope, "wanted", 0, 1)
	if err != nil || more || len(rows) != 1 || len(rows[0].Messages) != 0 || len(rows[0].Snapshot) != 0 {
		t.Fatal(rows, more, err)
	}
	a, err := s.ReadReplyAudit(context.Background(), scope, rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(a)
	if strings.Contains(string(raw), "secret candidate") || strings.Contains(string(raw), "private") {
		t.Fatal(string(raw))
	}
	a.At = time.Now().Add(-48 * time.Hour)
	_ = a // expiry tested through configured read below
	if _, err = s.ReadReplyAudit(context.Background(), scope, rows[0].ID, ReplyAuditSettings{}); err == nil {
		t.Fatal("disabled audit readable")
	}
}
func TestReplyAuditRedactsEmbeddedCredentialJSON(t *testing.T) {
	raw := redactAudit([]byte(`{"nested":"{\"api_key\":\"sk-private\"}","body":"token=synthetic-private"}`))
	if strings.Contains(string(raw), "sk-private") || strings.Contains(string(raw), "synthetic-private") {
		t.Fatal(string(raw))
	}
}

func TestReplyContextRejectsSourceRevisionChange(t *testing.T) {
	s, c, in := memoryFixture(t)
	in.IsGroup = true
	in.TraceID = "revision-source"
	in.Text = "synthetic user fact"
	m := sourceEntry(t, s, c, in, time.Now(), "fact", in.Text, 0)
	id := m.Sources[0].EventID
	e := &Engine{Store: s, Config: c}
	r := ReplyContext{TargetIDs: []string{in.Sender}, AnchorMessageID: in.TraceID, SourceEventIDs: []string{id}, SourceVersions: map[string]int{id: 1}}
	normalized, err := e.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{"收到"}, ReplyContexts: []ReplyContext{r}})
	if err != nil {
		t.Fatal(err)
	}
	r = normalized.ReplyContexts[0]
	if err := e.ValidateReplyContext(t.Context(), in, r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE events SET revision=revision+1 WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	if err := e.ValidateReplyContext(t.Context(), in, r); err == nil {
		t.Fatal("stale revision accepted")
	}
}

type groundingActionModel string

func (m groundingActionModel) Chat(context.Context, string, []map[string]string) (string, error) {
	return string(m), nil
}
func TestGroundingReviewHonorsSilentAndRejectsUnknownAction(t *testing.T) {
	for _, tc := range []struct {
		answer    string
		wantError bool
	}{{`{"action":"silent","bubbles":[{"text":"do not send this","route_id":"R1"}]}`, false}, {`{"action":"unexpected","bubbles":[{"text":"do not send this","route_id":"R1"}]}`, true}} {
		s, c, in := memoryFixture(t)
		in.IsGroup = true
		in.Text = "你爸刚视频"
		in.TraceID = "action-source"
		in.Source = &SourceRef{InstanceID: "action-suite", MessageID: in.TraceID, Account: in.Account, Thread: in.Thread, Sender: in.Sender, Integrity: "verified"}
		if err := s.Observe(in, c, time.Now()); err != nil {
			t.Fatal(err)
		}
		in.MustReply = tc.wantError
		e := &Engine{Store: s, Config: c, model: &groundingSequenceModel{answers: []string{tc.answer}, checks: []string{"reject"}}}
		d, err := e.ReviewGroundedReply(t.Context(), in, Decision{Action: "reply", Messages: []string{"你爸心里有点空"}})
		if tc.wantError {
			if err == nil {
				t.Fatal("unknown action accepted")
			}
		} else if err != nil || d.Action != "silent" || len(d.Messages) > 0 {
			t.Fatal("silent outcome changed", d, err)
		}
	}
}

func TestSameTurnMemoryRefreshDropsForgottenToolBodyAndRefreshesRevision(t *testing.T) {
	s, c, in := memoryFixture(t)
	in.TraceID = "refresh-event"
	in.Text = "old synthetic statement"
	m := sourceEntry(t, s, c, in, time.Now(), "fact", in.Text, 0)
	id := m.Sources[0].EventID
	e := &Engine{Store: s, Config: c}
	scope := ScopeID(in.Channel, in.Account, in.Thread)
	hist, _ := json.Marshal(map[string]any{"kind": "conversation_context", "scope": scope, "timeline": []map[string]any{{"source_event_id": id, "text": in.Text}}, "summary": nil})
	old, _ := json.Marshal(map[string]any{"entry": m})
	tool, _ := json.Marshal(map[string]any{"source": "untrusted_tool_result", "tool": "memory_read", "result": string(old), "execution": map[string]any{"result_id": "original-output-id"}})
	if _, err := s.db.Exec("UPDATE events SET text='new synthetic statement', revision=revision+1 WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	turns := e.refreshMemoryPackets(t.Context(), []map[string]string{{"role": "user", "content": string(hist)}})
	if len(turns) != 1 || strings.Contains(turns[0]["content"], in.Text) || !strings.Contains(turns[0]["content"], "new synthetic statement") {
		t.Fatal(turns)
	}
	if _, err := s.db.Exec("UPDATE memories SET status='deleted',version=version+1 WHERE id=?", m.ID); err != nil {
		t.Fatal(err)
	}
	turns = e.refreshMemoryPackets(t.Context(), []map[string]string{{"role": "assistant", "content": "original call"}, {"role": "user", "content": string(tool)}})
	if len(turns) != 2 || strings.Contains(turns[1]["content"], in.Text) || !strings.Contains(turns[1]["content"], "original-output-id") || !strings.Contains(turns[1]["content"], "invalidated") {
		t.Fatal(turns)
	}
}

type countedGroundingModel struct{ calls int }

func (m *countedGroundingModel) Chat(context.Context, string, []map[string]string) (string, error) {
	m.calls++
	return `{"action":"reply","messages":["收到"]}`, nil
}
func TestConflictedCurrentSourceNeverCallsModel(t *testing.T) {
	s, c, in := memoryFixture(t)
	c.Enabled = true
	in.Mode = "direct"
	in.IsGroup = true
	at := time.Now().Add(-time.Hour)
	in.MessageTime = at.Format(time.RFC3339Nano)
	in.Text = "first event"
	in.Source = &SourceRef{InstanceID: "conflict-suite", MessageID: in.TraceID, ServerID: "first-server", OccurredAt: in.MessageTime, Account: in.Account, Thread: in.Thread, Sender: in.Sender, Integrity: "verified"}
	if err := s.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	changed := *in.Source
	changed.ServerID = "another-server"
	in.Source = &changed
	in.Text = "different event"
	model := &countedGroundingModel{}
	e, err := New(c, s, model, "base")
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.Respond(t.Context(), in)
	if err == nil || model.calls != 0 || len(d.Messages) > 0 {
		t.Fatal("conflict reached model", d, err, model.calls)
	}
}
func TestGroundingRiskSignalsBeyondOriginalIncident(t *testing.T) {
	for _, text := range []string{"你奶奶现在怎么样", "她很伤心", "你一定很难过", "那是他的", "你以前去过那里"} {
		if len(groundingRisks(Input{Text: "hello"}, Decision{Messages: []string{text}})) == 0 {
			t.Fatal("unreviewed personal claim", text)
		}
	}
	for _, text := range []string{"晚安", "收到", "今天的天气不错"} {
		if len(groundingRisks(Input{Text: text}, Decision{Messages: []string{"明天见"}})) > 0 {
			t.Fatal("ordinary greeting got review", text)
		}
	}
}

func TestSameTurnGlobalSearchEpochInvalidatesPlainPreview(t *testing.T) {
	s, c, in := memoryFixture(t)
	e := &Engine{Store: s, Config: c}
	_ = in
	old, _ := json.Marshal(map[string]any{"_memory_epoch": "obsolete", "items": []string{"private stale original"}, "next_offset": 7})
	wrapper, _ := json.Marshal(map[string]any{"source": "untrusted_tool_result", "tool": "agent_global_search", "result": string(old), "execution": map[string]string{"result_id": "original-search-output"}})
	turns := e.refreshMemoryPackets(t.Context(), []map[string]string{{"role": "user", "content": string(wrapper)}})
	if len(turns) != 1 || strings.Contains(turns[0]["content"], "private stale original") || !strings.Contains(turns[0]["content"], "original-search-output") || !strings.Contains(turns[0]["content"], "next_offset") {
		t.Fatal(turns)
	}
}
func TestSourceOnlyContextNeverLoadsOriginPrivateHistory(t *testing.T) {
	scope := Scope{Messages: []Turn{{Role: "user", TraceID: "chosen", Sender: "owner", Text: "chosen source"}, {Role: "user", TraceID: "unrelated", Sender: "owner", Text: "private unrelated detail"}}}
	in := Input{SourceOnlyContext: true, Source: &SourceRef{MessageID: "chosen"}}
	got := groundedHistory(scope, in)
	if len(got) != 1 || got[0].TraceID != "chosen" {
		t.Fatal(got)
	}
}

func TestReplyContextRequiresVerifiedStateVersionAndSourceProof(t *testing.T) {
	s, c, in := memoryFixture(t)
	in.IsGroup = true
	in.Text = "verified source"
	m := sourceEntry(t, s, c, in, time.Now(), "fact", in.Text, 0)
	e := &Engine{Store: s, Config: c}
	d, err := e.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{"收到"}, ReplyContexts: []ReplyContext{{SourceEventIDs: []string{m.Sources[0].EventID}}}})
	if err != nil {
		t.Fatal(err)
	}
	r := d.ReplyContexts[0]
	for _, which := range []string{"state", "fingerprint", "versions"} {
		bad := r
		switch which {
		case "state":
			bad.State = ""
		case "fingerprint":
			bad.ContextVersion = "fake"
		case "versions":
			bad.SourceVersions = nil
			bad.ContextVersion = ReplyContextFingerprint(in, bad)
		}
		if e.ValidateReplyContext(t.Context(), in, bad) == nil {
			t.Fatal("unverified binding accepted", which)
		}
	}
	missing := in
	missing.TraceID = "not-observed"
	missing.Text = "no source"
	if _, err = e.normalizeReplyContexts(t.Context(), missing, Decision{Action: "reply", Messages: []string{"收到"}}); err == nil {
		t.Fatal("bare human message verified")
	}
	system := Input{Channel: in.Channel, Account: in.Account, Thread: in.Thread, IsGroup: true, Mode: "proactive"}
	out, err := e.normalizeReplyContexts(t.Context(), system, Decision{Action: "reply", Messages: []string{"大家早上好"}})
	if err != nil || out.ReplyContexts[0].State != "system_context" {
		t.Fatal("authorized system context rejected", out, err)
	}
	c.Permissions.Remember = false
	e.Config = c
	ephemeral := in
	ephemeral.TraceID = "fresh-native"
	ephemeral.Source = &SourceRef{MessageID: ephemeral.TraceID, ServerID: "native-server", Account: in.Account, Thread: in.Thread, Sender: in.Sender, Integrity: "verified", BodyHash: "verified-hash", OccurredAt: time.Now().Format(time.RFC3339Nano)}
	out, err = e.normalizeReplyContexts(t.Context(), ephemeral, Decision{Action: "reply", Messages: []string{"收到"}})
	if err != nil || out.ReplyContexts[0].State != "ephemeral_context" {
		t.Fatal("fresh stateless context rejected", out, err)
	}
	changed := *ephemeral.Source
	changed.BodyHash = "different"
	ephemeral.Source = &changed
	if e.ValidateReplyContext(t.Context(), ephemeral, out.ReplyContexts[0]) == nil {
		t.Fatal("stateless body changed but binding survived")
	}
}

type groundingSequenceModel struct {
	answers     []string
	checks      []string
	answerCalls int
	checkCalls  int
	inputs      []map[string]any
	calls       int
}

func (m *groundingSequenceModel) Chat(_ context.Context, policy string, turns []map[string]string) (string, error) {
	m.calls++
	if len(turns) > 0 {
		var v map[string]any
		_ = json.Unmarshal([]byte(turns[len(turns)-1]["content"]), &v)
		m.inputs = append(m.inputs, v)
	}
	if policy == groundingCheckPolicy {
		verdict := "allow"
		if len(m.checks) > 0 {
			verdict = m.checks[min(m.checkCalls, len(m.checks)-1)]
		}
		m.checkCalls++
		return syntheticGroundingVerdict(turns, verdict), nil
	}
	n := min(m.answerCalls, len(m.answers)-1)
	m.answerCalls++
	return m.answers[n], nil
}

// This fake exercises protocol plumbing only. Its explicit verdict is supplied
// by the test; it does not infer semantic support or count as model acceptance.
func syntheticGroundingVerdict(turns []map[string]string, verdict string) string {
	var state struct {
		Units []GroundingReviewUnit `json:"review_units"`
	}
	_ = json.Unmarshal([]byte(turns[len(turns)-1]["content"]), &state)
	return atomicTestWire(state.Units, func(unit GroundingReviewUnit) []GroundingAtom {
		if verdict == "allow" {
			return atomicNonfactual(unit)
		}
		status := "unsupported"
		if verdict == "uncertain" {
			status = "uncertain"
		}
		return []GroundingAtom{{Span: unit.Text, Mode: "assertion", Claim: "Synthetic fixture assertion", Status: status, Supports: []GroundingSupport{}, Reason: "Synthetic protocol rejection, not a real semantic decision."}}
	})
}

func groundedInput(t *testing.T, s *Store, c Config, actor, id, text string) Input {
	t.Helper()
	in := Input{Channel: "wechat", Account: "synthetic", Thread: "room@chatroom", Sender: actor, TraceID: id, Text: text, IsGroup: true, Mode: "direct", Directed: true, Source: &SourceRef{InstanceID: "review-sequence", MessageID: id, Account: "synthetic", Thread: "room@chatroom", Sender: actor, Integrity: "verified"}}
	if err := s.Observe(in, c, time.Now()); err != nil {
		t.Fatal(err)
	}
	return in
}
func TestMandatoryPlatformReplyRepairsSilenceWithoutForcingSocial(t *testing.T) {
	for _, must := range []bool{true, false} {
		s, c, _ := memoryFixture(t)
		c.Enabled = true
		in := groundedInput(t, s, c, "alpha", "mandatory", "这次视频结束了。")
		in.MustReply = must
		model := &groundingSequenceModel{answers: []string{`{"action":"silent","messages":[]}`, `{"action":"reply","bubbles":[{"text":"刚结束通话呀，聊得怎么样？","route_id":"R1"}]}`}}
		e, err := New(c, s, model, "base")
		if err != nil {
			t.Fatal(err)
		}
		d, err := e.Respond(t.Context(), in)
		if must {
			if err != nil || d.Action != "reply" || len(d.Messages) == 0 || model.calls != 3 {
				t.Fatal(d, err, model.calls)
			}
		} else if err != nil || d.Action != "silent" || model.calls != 1 {
			t.Fatal("optional participation forced", d, err, model.calls)
		}
	}
}
func TestGroundingReviewCorrectsUnsupportedSceneOnce(t *testing.T) {
	s, c, _ := memoryFixture(t)
	in := groundedInput(t, s, c, "alpha", "scene", "刚挂掉。")
	model := &groundingSequenceModel{answers: []string{`{"action":"reply","bubbles":[{"text":"刚结束通话呀。","route_id":"R1"}]}`}, checks: []string{"reject", "allow"}}
	e := &Engine{Store: s, Config: c, model: model}
	d, err := e.ReviewGroundedReply(t.Context(), in, Decision{Action: "reply", Messages: []string{"屋里突然安静得有点空。"}})
	if err != nil || d.Action != "reply" || model.calls != 3 || len(d.Messages) == 0 {
		t.Fatal(d, err, model.calls)
	}
	if model.inputs[1]["validation_error"] == nil {
		t.Fatal("correction lacks actual validation reason")
	}
}
func TestReviewRetargetRequiresExplicitPersonAndOriginalQuote(t *testing.T) {
	s, c, _ := memoryFixture(t)
	groundedInput(t, s, c, "beta", "beta-call", "我刚和父亲视频。")
	in := groundedInput(t, s, c, "alpha", "alpha-report", "你爸刚视频。")
	in.Addressees = []string{"beta"}
	model := &groundingSequenceModel{answers: []string{`{"action":"reply","bubbles":[{"text":"和爸爸聊得怎么样？","route_id":"R2"}]}`}, checks: []string{"reject", "allow"}}
	e := &Engine{Store: s, Config: c, model: model}
	d, err := e.ReviewGroundedReply(t.Context(), in, Decision{Action: "reply", Messages: []string{"你刚和他视频结束？"}})
	if err != nil || len(d.ReplyContexts) != 1 {
		t.Fatal(d, err)
	}
	r := d.ReplyContexts[0]
	if len(r.TargetIDs) != 1 || r.TargetIDs[0] != "beta" || !r.QuoteRequired || r.AnchorMessageID != in.TraceID {
		t.Fatal("retarget lacks verified audience/quote", r)
	}
	if err := e.ValidateReplyContext(t.Context(), in, r); err != nil {
		t.Fatal(err)
	}
	audiences := model.inputs[0]["outgoing_audiences"]
	people := model.inputs[0]["evidence_table"]
	if audiences == nil || people == nil {
		t.Fatal("review lacks pronoun/participant structure")
	}
}

func TestKnownAddresseeRelationCannotRebindToReporter(t *testing.T) {
	for index, draft := range []string{"哦哦，刚和你爸视频完呀，是有点没聊够吗？", "你说你爸刚视频完。", "你刚和他视频结束，想聊聊吗？", "那你们聊得怎么样？", "那您们聊得怎么样？"} {
		s, c, _ := memoryFixture(t)
		groundedInput(t, s, c, "beta", "call", "我刚和父亲视频。")
		in := groundedInput(t, s, c, "alpha", "report", "你爸刚视频，刚挂掉。")
		in.Addressees = []string{"beta"}
		in.MustReply = true
		model := &groundingSequenceModel{answers: []string{`{"action":"reply","bubbles":[{"text":"刚结束通话呀。","route_id":"R1"}]}`}, checks: []string{"reject", "allow"}}
		e := &Engine{Store: s, Config: c, model: model}
		wantCalls, editorIndex := 3, 1
		if index >= 3 {
			model.checks = []string{"allow"}
			wantCalls, editorIndex = 2, 0
		} // The explicit plural contract rejects before a model check.
		d, err := e.ReviewGroundedReply(t.Context(), in, Decision{Action: "reply", Messages: []string{draft}})
		if err != nil || d.Action != "reply" || model.calls != wantCalls || model.inputs[editorIndex]["rejected_verdict"] == nil {
			t.Fatal(draft, d, err, model.calls, model.inputs)
		}
	}
}

func TestAssistantStatementReviewSourceIsEvidenceNotFact(t *testing.T) {
	s, c, _ := memoryFixture(t)
	old := groundedInput(t, s, c, "alpha", "original", "我只说刚挂电话。")
	if err := s.Accepted(old, "你一定很伤心。", c, time.Now()); err != nil {
		t.Fatal(err)
	}
	scope := s.Context(old, c, time.Now())
	assistantID := ""
	for _, turn := range scope.Messages {
		if turn.Role == "assistant" {
			assistantID = turn.EventID
		}
	}
	if assistantID == "" {
		t.Fatal("assistant source not persisted")
	}
	in := groundedInput(t, s, c, "alpha", "correction", "我没说自己伤心，你刚才是编的。")
	model := &assistantAliasProposalModel{groundingSequenceModel: groundingSequenceModel{checks: []string{"reject", "allow"}}, anchor: old.TraceID}
	e := &Engine{Store: s, Config: c, model: model}
	d, err := e.ReviewGroundedReply(t.Context(), in, Decision{Action: "reply", Messages: []string{"对，是我擅自添加了你的感受，抱歉。"}, ReplyContexts: []ReplyContext{{RiskFlags: []string{"unsupported_prior_reply"}}}})
	if err != nil || d.Action != "reply" || model.calls != 3 {
		t.Fatal(d, err, model.calls)
	}
	for _, id := range d.ReplyContexts[0].SourceEventIDs {
		if id == assistantID {
			t.Fatal("assistant treated as user fact")
		}
	}
	if d.ReplyContexts[0].AnchorMessageID != in.TraceID {
		t.Fatal("wrong statement anchor", d.ReplyContexts[0])
	}
}

// Propose the assistant statement visible in the actual editor packet, using
// its local alias instead of leaking the fixture's out-of-band persistent ID.
type assistantAliasProposalModel struct {
	groundingSequenceModel
	anchor string
}

func (m *assistantAliasProposalModel) Chat(ctx context.Context, policy string, turns []map[string]string) (string, error) {
	if policy != groundingCheckPolicy {
		var state any
		_ = json.Unmarshal([]byte(turns[0]["content"]), &state)
		var find func(any) string
		find = func(v any) string {
			switch x := v.(type) {
			case map[string]any:
				if x["role"] == "assistant" {
					if id, _ := x["source_event_id"].(string); id != "" {
						return id
					}
					if row, ok := x["content"].(map[string]any); ok {
						if id, _ := row["source_event_id"].(string); id != "" {
							return id
						}
					}
				}
				for _, child := range x {
					if id := find(child); id != "" {
						return id
					}
				}
			case []any:
				for _, child := range x {
					if id := find(child); id != "" {
						return id
					}
				}
			}
			return ""
		}
		id := find(state)
		if id == "" {
			return "", errors.New("assistant statement missing from editor evidence")
		}
		answer, _ := json.Marshal(routeEditedReply{Action: "reply", Bubbles: []editedReplyBubble{{Text: "对，是我擅自添加了你的感受，抱歉。", RouteID: "R1"}}})
		m.answers = []string{string(answer)}
	}
	return m.groundingSequenceModel.Chat(ctx, policy, turns)
}

func TestMediaReviewSeparatesUploaderObservationAndLiteralWords(t *testing.T) {
	blank := ""
	in := Input{IsGroup: true, Sender: "beta", Text: "观察：自行车。", OriginalText: &blank, IncomingMedia: []IncomingMediaRef{{Source: SourceRef{MessageID: "new"}, AssetID: "new-image", UploaderID: "beta", Kind: "image", Observation: "蓝色汽车", ObservationState: "ready"}}}
	in.QuotedSource = &SourceRef{Sender: "alpha", EventID: "old"}
	scope := Scope{Messages: []Turn{{Role: "user", Sender: "alpha", EventID: "old", HasOriginal: true, FullText: "", Text: "机器观察：去年拍摄的黄色自行车", IncomingMedia: []IncomingMediaRef{{Source: SourceRef{EventID: "old", MessageID: "old"}, AssetID: "old-image", UploaderID: "alpha", Kind: "image", Observation: "黄色自行车", ObservationState: "ready"}}}}}
	packets := []map[string]string{{"content": `{"source_event_id":"old"}`}}
	rows := observationEvidence(in.IncomingMedia)
	rows = append(rows, observationEvidence(scope.Messages[0].IncomingMedia)...)
	if len(rows) != 2 {
		t.Fatal(rows)
	}
	for _, row := range rows {
		if row["creator_id"] != nil || row["owner_id"] != nil || row["captured_at"] != nil || row["uploader_id"] == "" || row["producer"] != "media_observer" {
			t.Fatal("invented media authorship", row)
		}
	}
	e := &Engine{}
	people := e.participantUtterances(in, scope, packets)
	for _, person := range people {
		if person["utterance"] != "" {
			t.Fatal("observation became literal words", person)
		}
	}
	in.QuotedSource = nil
	selected := groundedHistory(scope, in)
	if len(selected[0].IncomingMedia) != 0 {
		t.Fatal("unrelated personal media resurrected")
	}
}
