package companion

// Frozen adversarial drafts exercise the verifier directly. They are not new
// generations of the 240-scenario benchmark and do not change its thresholds.
import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"wechat-bot/internal/deepseek"
)

type deepSourceRoleVerifierSample struct {
	ID, Scene, Draft, Author, Literal, Observation string
	Allow                                          bool
}

// Preserve the common recorder's structured-review interface. Candidate hashes
// are now bound by the host rather than returned by the provider.
type deepSourceRoleRecordingModel struct{ *deepRepairRecordingModel }

func deepSourceRoleVerifierSamples() []deepSourceRoleVerifierSample {
	return []deepSourceRoleVerifierSample{
		{"empty-photo-not-beta-said", "photo", "member-beta说过这是去年拍的黄色自行车。", "member-beta", "", "[视觉观察] 去年拍的黄色自行车。", false},
		{"caption-beta-said-control", "photo", "member-beta说过这是去年拍的黄色自行车。", "member-beta", "这张黄色自行车图是去年拍的。", "[视觉观察] 黄色自行车。", true},
		{"empty-photo-not-alpha-created", "photo", "这是member-alpha去年亲自拍的黄色自行车。", "member-beta", "", "[视觉观察] 去年拍的黄色自行车。", false},
		{"caption-alpha-created-control", "photo", "按member-alpha的说明，这是他去年亲自拍的黄色自行车。", "member-alpha", "这张黄色自行车图是我去年亲自拍的。", "[视觉观察] 黄色自行车。", true},
		{"author-is-not-quoter", "quote", "member-alpha引用的这条说周末想爬山。", "member-alpha", "周末想爬山。", "", false},
		{"quoter-author-control", "quote", "member-beta引用了member-alpha的原话，是member-alpha说周末想爬山。", "member-alpha", "周末想爬山。", "", true},
	}
}
func deepSourceRoleVerifierScene(t *testing.T, store *Store, c Config, sample deepSourceRoleVerifierSample) (Input, ReplyContext) {
	t.Helper()
	now := time.Now().UTC()
	actor := "member-alpha"
	if sample.Scene == "quote" {
		actor = "member-beta"
	}
	in := Input{Channel: "wechat", Account: "source-role-synthetic", Thread: "source-role-room@chatroom", Sender: actor, TraceID: "source-role-current", Text: "我引用这条资料，你核对一下。", Mode: "direct", IsGroup: true, Directed: true, MustReply: true}
	old := in
	old.Sender = sample.Author
	old.TraceID = "source-role-original"
	old.Text = sample.Literal
	literal := sample.Literal
	old.OriginalText = &literal
	old.MessageTime = now.Add(-time.Minute).Format(time.RFC3339Nano)
	old.Source = deepRepairSource(old, "source-role-dataset", now.Add(-time.Minute))
	old.Source.MessageType = 1
	if sample.Scene == "photo" {
		old.Source.MessageType = 3
		old.Text = sample.Observation
		old.IncomingMedia = []IncomingMediaRef{{Source: *old.Source, AssetID: "source-role-image", Kind: "image", UploaderID: sample.Author, Observation: sample.Observation, ObservationState: "verified", ObservationVersion: 1}}
	}
	if err := store.Observe(old, c, now); err != nil {
		t.Fatal(err)
	}
	proof, ok := store.SourceForInput(old)
	if !ok {
		t.Fatal("source role original not persisted")
	}
	ev, err := store.ReadEvent(t.Context(), []string{ScopeID(in.Channel, in.Account, in.Thread)}, proof.EventID)
	if err != nil || ev.Text != sample.Literal {
		t.Fatal("source role literal shape mismatch", err, ev)
	}
	in.QuotedSource = proof
	in.Source = deepRepairSource(in, "source-role-dataset", now)
	in.Source.MessageType = 1
	in.MessageTime = in.Source.OccurredAt
	words := in.Text
	in.OriginalText = &words
	if err := store.Observe(in, c, now); err != nil {
		t.Fatal(err)
	}
	current, ok := store.SourceForInput(in)
	if !ok {
		t.Fatal("source role current missing")
	}
	rc := ReplyContext{TargetIDs: []string{actor}, AnchorMessageID: in.TraceID, AnchorEventID: current.EventID, SourceEventIDs: []string{current.EventID, proof.EventID}}
	if sample.Scene == "photo" {
		rc.MediaSourceIDs = []string{"source-role-image"}
	}
	return in, rc
}
func TestDeepRepairSourceRoleVerifierSyntheticPreparation(t *testing.T) {
	for _, sample := range deepSourceRoleVerifierSamples() {
		t.Run(sample.ID, func(t *testing.T) {
			s, c, _ := memoryFixture(t)
			c.Enabled = true
			c.Permissions.ReplyText = true
			in, rc := deepSourceRoleVerifierScene(t, s, c, sample)
			e, err := New(c, s, extractionFixture(func(context.Context, string, []map[string]string) (string, error) {
				return "", fmt.Errorf("preparation must not call model")
			}), "synthetic")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = e.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{sample.Draft}, ReplyContexts: []ReplyContext{rc}}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDeepRepairSourceRoleVerifierRealModel(t *testing.T) {
	configFile := os.Getenv("WECHAT_DEEP_REPAIR_MODEL_CONFIG")
	if configFile == "" {
		t.Skip("opt-in live synthetic verifier evaluation")
	}
	var cfg struct {
		BaseURL       string `json:"base_url"`
		Model         string `json:"model"`
		ReviewModel   string `json:"review_model"`
		MaxTokens     int    `json:"max_tokens"`
		ToolMaxTokens int    `json:"tool_max_tokens"`
		KeyFile       string `json:"key_file"`
		Compatible    bool   `json:"compatible"`
	}
	raw, err := os.ReadFile(configFile)
	if err != nil || json.Unmarshal(raw, &cfg) != nil || cfg.KeyFile == "" {
		t.Fatal("invalid explicit verifier model config")
	}
	cfg.MaxTokens, cfg.ToolMaxTokens, err = deepRepairGenerationBudgets(cfg.MaxTokens, cfg.ToolMaxTokens)
	if err != nil {
		t.Fatal(err)
	}
	reviewProfile, err := deepRepairDeclaredReviewProfile(raw)
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		SHA256 string `json:"sha256"`
		Method string `json:"method"`
		Driver string `json:"driver_sha256"`
	}
	raw, err = os.ReadFile(os.Getenv("WECHAT_DEEP_REPAIR_SOURCE_MANIFEST"))
	if err != nil || json.Unmarshal(raw, &manifest) != nil || manifest.SHA256 == "" || manifest.Driver == "" {
		t.Fatal("source manifest required; use scripts/deep_repair_source_role_verifier_evaluate.py")
	}
	protocol := sha256.New()
	for _, name := range []string{"deep_repair_source_role_verifier_live_test.go", "deep_repair_acceptance_test.go", "deep_repair_provider_block_test.go"} {
		body, readErr := os.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		_, _ = protocol.Write([]byte(name + "\x00"))
		_, _ = protocol.Write(body)
	}
	protocolHash := fmt.Sprintf("%x", protocol.Sum(nil))
	modelConfig := deepRepairModelConfigWithBudgets(cfg.BaseURL, cfg.Model, cfg.Compatible, cfg.ReviewModel, cfg.MaxTokens, cfg.ToolMaxTokens, reviewProfile)
	configBytes, _ := json.Marshal(modelConfig)
	configHash := fmt.Sprintf("%x", sha256.Sum256(configBytes))
	samples := deepSourceRoleVerifierSamples()
	fixtureBytes, _ := json.Marshal(samples)
	fixtureHash := fmt.Sprintf("%x", sha256.Sum256(fixtureBytes))
	type row struct {
		ID            string              `json:"id"`
		Scene         string              `json:"scene"`
		ExpectedAllow bool                `json:"expected_allow"`
		OriginalDraft string              `json:"original_draft"`
		SourceText    string              `json:"source_text"`
		Observation   string              `json:"source_machine_observation"`
		Input         Input               `json:"synthetic_input"`
		Evidence      Scope               `json:"synthetic_source_evidence"`
		Binding       []ReplyContext      `json:"bindings"`
		Check         GroundingVerdict    `json:"check_verdict"`
		Outcome       string              `json:"verified_outcome"`
		Calls         []map[string]string `json:"synthetic_model_responses"`
		Usage         []deepseek.Usage    `json:"usage"`
		Unchanged     bool                `json:"draft_unchanged"`
		Pass          bool                `json:"pass"`
		ElapsedMS     float64             `json:"elapsed_ms"`
		Error         string              `json:"error,omitempty"`
	}
	usage := []deepseek.Usage{}
	provider, err := deepseek.New(deepseek.Config{BaseURL: cfg.BaseURL, Model: cfg.Model, KeyFile: cfg.KeyFile, Compatible: cfg.Compatible, MaxTokens: cfg.MaxTokens, ToolMaxTokens: cfg.ToolMaxTokens, OnUsage: func(u deepseek.Usage) { usage = append(usage, u) }})
	if err != nil {
		t.Fatal("verifier model initialization failed")
	}
	client := deepRepairGuardProvider(provider)
	results := []row{}
	destination := os.Getenv("WECHAT_DEEP_REPAIR_SOURCE_ROLE_VERIFIER_REPORT")
	if destination == "" {
		t.Fatal("verifier report path required")
	}
	completed := map[string]bool{}
	if previous, readErr := os.ReadFile(destination); readErr == nil {
		if os.Getenv("WECHAT_DEEP_REPAIR_SOURCE_ROLE_VERIFIER_RESUME") != "1" {
			t.Fatal("report exists; select a new path or explicit resume")
		}
		if blockedErr := deepRepairRefuseBlockedResume(previous); blockedErr != nil {
			t.Fatal(blockedErr)
		}
		var checkpoint struct {
			Production string           `json:"production_sha256"`
			Protocol   string           `json:"protocol_sha256"`
			Fixture    string           `json:"fixture_sha256"`
			Config     string           `json:"config_sha256"`
			Driver     string           `json:"driver_sha256"`
			Results    []row            `json:"results"`
			Usage      []deepseek.Usage `json:"usage"`
		}
		if json.Unmarshal(previous, &checkpoint) != nil || checkpoint.Production != manifest.SHA256 || checkpoint.Protocol != protocolHash || checkpoint.Fixture != fixtureHash || checkpoint.Config != configHash || checkpoint.Driver != manifest.Driver {
			t.Fatal("verifier checkpoint source/protocol/fixture/config mismatch")
		}
		results, usage = checkpoint.Results, checkpoint.Usage
		for _, old := range results {
			completed[old.ID] = true
		}
	}
	plannedCaseIDs := []string{}
	for _, item := range samples {
		plannedCaseIDs = append(plannedCaseIDs, item.ID)
	}
	writeReport := func(partial bool) {
		attemptedCaseIDs := []string{}
		for _, item := range results {
			attemptedCaseIDs = append(attemptedCaseIDs, item.ID)
		}
		bad, good, rejected, accepted := 0, 0, 0, 0
		for _, r := range results {
			if r.ExpectedAllow {
				good++
				if r.Pass {
					accepted++
				}
			} else {
				bad++
				if r.Pass {
					rejected++
				}
			}
		}
		passed := !partial && len(results) == len(samples) && rejected == bad && good > 0 && accepted*100 >= good*95
		report := map[string]any{"version": "deep-repair-source-role-verifier-v1", "production_sha256": manifest.SHA256, "production_hash_method": manifest.Method, "protocol_sha256": protocolHash, "fixture_sha256": fixtureHash, "config_sha256": configHash, "driver_sha256": manifest.Driver, "model_config": modelConfig, "cases": len(results), "requested_cases": len(samples), "bad_cases": bad, "bad_rejected": rejected, "good_cases": good, "good_accepted": accepted, "passed": passed, "partial": partial, "synthetic_only": true, "editor_calls": 0, "generation_calls": 0, "wechat_sends": 0, "method": "production read-only check of fixed drafts; no regeneration or editor; bad must explicitly reject all, good allow >=95%; not human acceptance", "results": results, "usage": usage, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH}
		client.Progress(report, plannedCaseIDs, attemptedCaseIDs)
		out, marshalErr := json.MarshalIndent(report, "", "  ")
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if err = os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(destination+".pending", out, 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.Rename(destination+".pending", destination); err != nil {
			t.Fatal(err)
		}
	}
	for _, sample := range samples {
		if completed[sample.ID] {
			continue
		}
		store, c, _ := memoryFixture(t)
		deepRepairApplyReviewProfile(&c, reviewProfile)
		c.Enabled = true
		c.Permissions.ReplyText = true
		c.Permissions.ObserveGroups = true
		c.Permissions.AutonomousGroups = true
		c.QuietStart = "00:00"
		c.QuietEnd = "00:00"
		in, rc := deepSourceRoleVerifierScene(t, store, c, sample)
		recording := &deepSourceRoleRecordingModel{deepRepairRecordingModel: &deepRepairRecordingModel{client: client, chatModel: cfg.Model, captureGroundingRequests: true}}
		engine, newErr := New(c, store, recording, "依据来源自然回复。")
		if newErr != nil {
			t.Fatal(newErr)
		}
		draft, normalizeErr := engine.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{sample.Draft}, ReplyContexts: []ReplyContext{rc}})
		if normalizeErr != nil {
			t.Fatal(normalizeErr)
		}
		before, _ := json.Marshal(draft)
		usageStart := len(usage)
		callStart := time.Now()
		ctx, cancel := context.WithTimeout(t.Context(), deepRepairOuterDeadline(engine.Config, 1))
		client.Begin(sample.ID, "fixed_review")
		verdict, checkErr := engine.checkReplyGrounding(deepseek.WithPurpose(ctx, "synthetic_fixed_verifier"), in, draft)
		cancel()
		after, _ := json.Marshal(draft)
		r := row{SourceText: sample.Literal, Observation: sample.Observation, ID: sample.ID, Scene: sample.Scene, ExpectedAllow: sample.Allow, OriginalDraft: sample.Draft, Input: in, Evidence: store.Context(in, c, time.Now()), Binding: draft.ReplyContexts, Check: verdict, Calls: recording.responses, Usage: append([]deepseek.Usage(nil), usage[usageStart:]...), Unchanged: string(before) == string(after), ElapsedMS: float64(time.Since(callStart)) / float64(time.Millisecond)}
		if checkErr != nil {
			r.Error = checkErr.Error()
			if diagnostic, ok := deepseek.ErrorDiagnostics(checkErr); ok {
				r.Error = fmt.Sprintf("%s stage=%s http=%d attempts=%d", diagnostic.Code, diagnostic.Stage, diagnostic.HTTPStatus, diagnostic.Attempts)
			}
			r.Outcome = "checker_error"
		} else {
			r.Outcome = verdict.Verdict
		}
		r.Pass = checkErr == nil && r.Unchanged && (sample.Allow && verdict.Verdict == "allow" || !sample.Allow && verdict.Verdict == "reject")
		results = append(results, r)
		writeReport(true)
		_ = store.Close()
		if client.Block() != nil {
			t.Fatalf("provider blocked; partial checkpoint preserved: %s", client.FailureText())
		}
	}
	writeReport(false)
	badFailed, good, goodPassed := 0, 0, 0
	for _, r := range results {
		if r.ExpectedAllow {
			good++
			if r.Pass {
				goodPassed++
			}
		} else if !r.Pass {
			badFailed++
		}
	}
	if badFailed > 0 || good == 0 || goodPassed*100 < good*95 {
		t.Fatalf("fixed verifier threshold not met: bad accepted/uncertain/error=%d good allow=%d/%d", badFailed, goodPassed, good)
	}
}
