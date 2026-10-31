package companion

// These are wire-adapter checks, not additional model quality stimuli. The
// original 44/240 drafts and semantic expectations remain unchanged.
import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/deepseek"
)

type deepAliasAwareProbe struct {
	deepStructuredFake
	Reference string
	Selected  string
	Packet    map[string]json.RawMessage
	Wire      string
}

func (m *deepAliasAwareProbe) ChatGrounding(_ context.Context, _ string, turns []map[string]string, options deepseek.GroundingOptions) (string, error) {
	m.grounding++
	m.options = options
	if len(turns) != 1 {
		return "", fmt.Errorf("one synthetic review packet required")
	}
	m.Wire = turns[0]["content"]
	if err := json.Unmarshal([]byte(m.Wire), &m.Packet); err != nil {
		return "", err
	}
	var table []GroundingEvidenceRow
	if err := json.Unmarshal(m.Packet["evidence_table"], &table); err != nil {
		return "", err
	}
	var selected *GroundingEvidenceRow
	m.Selected = m.Reference
	for i := range table {
		for _, ref := range table[i].SourceRefs {
			if m.Reference != "" && ref == m.Reference || m.Reference == "" && strings.HasPrefix(ref, "S") {
				selected = &table[i]
				m.Selected = ref
				break
			}
		}
		if selected != nil {
			break
		}
	}
	ref, quote := m.Selected, "Synthetic unknown reference"
	if selected != nil {
		ref, quote = selected.Ref, selected.Text
		if quote == "" {
			quote = groundingString(selected.Metadata["message_sender_id"])
		}
		if selected.Kind == "host_knowledge" {
			quote = "current_request_visible_results"
		}
	}
	var units []GroundingReviewUnit
	if err := json.Unmarshal(m.Packet["review_units"], &units); err != nil || len(units) == 0 {
		return "", fmt.Errorf("missing exact host review units")
	}
	results := []GroundingUnitVerdict{}
	for _, unit := range units {
		results = append(results, GroundingUnitVerdict{UnitID: unit.ID, Atoms: []GroundingAtom{{Span: unit.Text, Mode: "assertion", Claim: "Synthetic provenance protocol assertion", Status: "unsupported", Supports: []GroundingSupport{{Ref: ref, Quote: quote}}, Reason: "Synthetic wire-adapter reference probe; not semantic gold."}}})
	}
	response := map[string]any{"units": results}
	raw, err := json.Marshal(response)
	return string(raw), err
}

func deepAliasReviewBinding(t *testing.T, verdict GroundingVerdict) (map[string]string, json.RawMessage, json.RawMessage) {
	t.Helper()
	raw, err := json.Marshal(verdict)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Binding struct {
			Aliases  map[string]string `json:"source_aliases"`
			Original json.RawMessage   `json:"original_state"`
			Input    json.RawMessage   `json:"review_input"`
		} `json:"review_binding"`
	}
	if err = json.Unmarshal(raw, &result); err != nil || len(result.Binding.Aliases) == 0 || len(result.Binding.Original) == 0 || len(result.Binding.Input) == 0 {
		t.Fatal("host audit did not preserve source alias binding", err)
	}
	return result.Binding.Aliases, result.Binding.Original, result.Binding.Input
}

func TestDeepRepairAliasAwareRecorderUsesWireAndRestoresRealSources(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(fmt.Sprintf("nested-%v", nested), func(t *testing.T) {
			store, c, _ := memoryFixture(t)
			c.Enabled, c.Permissions.ReplyText = true, true
			profile := GroundingSettings{Model: "deepseek-v4-pro", MaxTokens: 32768, TimeoutSeconds: 240, ReasoningEffort: "high"}
			deepRepairApplyReviewProfile(&c, profile)
			in, rc := deepVerifierScene(t, store, c, "retelling")
			probe := &deepAliasAwareProbe{}
			recording := &deepRepairRecordingModel{client: probe, chatModel: "deepseek-flash", captureGroundingRequests: true}
			var model LanguageModel = recording
			if nested {
				model = &deepSourceRoleRecordingModel{deepRepairRecordingModel: recording}
			}
			engine, err := New(c, store, model, "synthetic")
			if err != nil {
				t.Fatal(err)
			}
			draft, err := engine.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{"只用于局部来源编号接口检查。"}, ReplyContexts: []ReplyContext{rc}})
			if err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(draft)
			verdict, err := engine.checkReplyGrounding(t.Context(), in, draft)
			after, _ := json.Marshal(draft)
			if err != nil || verdict.Verdict != "reject" || string(before) != string(after) {
				t.Fatal("wire adapter changed immutable candidate or failed", err)
			}
			aliases, original, input := deepAliasReviewBinding(t, verdict)
			real, ok := aliases[probe.Selected]
			if !ok || real == probe.Selected || len(real) != 64 {
				t.Fatal("selected call-local alias did not bind actual source", probe.Selected, real)
			}
			if !reflect.DeepEqual(verdict.Bubbles[0].Violations[0].SourceRefs, []string{real}) {
				t.Fatal("host verdict did not restore exact real source")
			}
			if strings.Contains(probe.Wire, real) || !strings.Contains(string(original), real) {
				t.Fatal("real source leaked into wire metadata or disappeared from host audit")
			}
			var projected map[string]json.RawMessage
			if json.Unmarshal(input, &projected) != nil || !reflect.DeepEqual(projected, probe.Packet) {
				t.Fatal("audited review input differs from actually transmitted packet")
			}
			for alias, id := range aliases {
				if strings.HasPrefix(alias, "S") && strings.Contains(probe.Wire, id) {
					t.Fatal("model received a hashed metadata source identifier")
				}
			}
			if recording.responses[0]["synthetic_request"] == "" || recording.responses[0]["actual_review_model"] != "deepseek-v4-pro" {
				t.Fatal("recorder omitted V12 wire or actual review model")
			}
			probe.Reference = "S999999"
			if _, err = engine.checkReplyGrounding(t.Context(), in, draft); err == nil {
				t.Fatal("unknown alias was approximately accepted")
			}
			probe.Reference = real
			if _, err = engine.checkReplyGrounding(t.Context(), in, draft); err == nil {
				t.Fatal("raw real ID bypassed call-local alias allowlist")
			}
		})
	}
}

func TestDeepRepairHostKnowledgeReferenceIsNotUserSource(t *testing.T) {
	sample := deepToolVerifierSamples()[1] // Existing generated-control remains expected allow in the live suite.
	engine, in := deepToolVerifierFixture(t, sample)
	profile := GroundingSettings{Model: "deepseek-v4-pro", MaxTokens: 32768, TimeoutSeconds: 240, ReasoningEffort: "high"}
	deepRepairApplyReviewProfile(&engine.Config, profile)
	probe := &deepAliasAwareProbe{Reference: "K1"}
	recording := &deepRepairRecordingModel{client: probe, chatModel: "deepseek-flash", captureGroundingRequests: true}
	engine.model = recording
	proof, ok := engine.Store.SourceForInput(in)
	if !ok {
		t.Fatal("synthetic request source missing")
	}
	draft, err := engine.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{"仅检查宿主知识状态引用接口。"}, ReplyContexts: []ReplyContext{{TargetIDs: []string{in.Sender}, SourceEventIDs: []string{proof.EventID}}}})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(engine.Store.Context(in, engine.Config, time.Now()))
	verdict, err := engine.checkReplyGrounding(t.Context(), in, draft)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(engine.Store.Context(in, engine.Config, time.Now()))
	if string(before) != string(after) {
		t.Fatal("host knowledge review wrote user memory")
	}
	aliases, _, _ := deepAliasReviewBinding(t, verdict)
	if aliases["K1"] != "K1" || !reflect.DeepEqual(verdict.Bubbles[0].Violations[0].SourceRefs, []string{"K1"}) {
		t.Fatal("host knowledge reference was mapped into a user source")
	}
	var knowledge map[string]any
	var table []GroundingEvidenceRow
	_ = json.Unmarshal(probe.Packet["evidence_table"], &table)
	for _, row := range table {
		if row.Kind == "host_knowledge" {
			_ = json.Unmarshal([]byte(row.Text), &knowledge)
		}
	}
	if knowledge["ref"] != "K1" {
		t.Fatal("actual wire omitted typed host request knowledge state")
	}
	for _, turn := range engine.Store.Context(in, engine.Config, time.Now()).Messages {
		if turn.EventID == "K1" {
			t.Fatal("derived host knowledge became a persisted chat event")
		}
	}
}
