package companion

// This independent set freezes the seven confirmed v7 failures, including every
// outgoing bubble and its actual sources. It does not alter the original 37 or
// 240 stimuli, expected outcomes, or acceptance thresholds.
import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/mcptools"
)

type deepV7FrozenCandidate struct {
	ID                 string   `json:"id"`
	ExpectedAllow      bool     `json:"expected_allow"`
	Violation          string   `json:"confirmed_violation"`
	Reason             string   `json:"evidence_reason"`
	Input              Input    `json:"original_input"`
	History            []Turn   `json:"original_authorized_history"`
	Decision           Decision `json:"original_decision"`
	OriginReportSHA256 string   `json:"origin_report_sha256"`
}

func deepV7FrozenCandidates(t *testing.T) ([]deepV7FrozenCandidate, []byte) {
	t.Helper()
	raw, err := os.ReadFile("testdata/deep_repair/v7_confirmed_candidates.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases     []deepV7FrozenCandidate `json:"cases"`
		Synthetic bool                    `json:"synthetic_only"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil || !fixture.Synthetic || len(fixture.Cases) != 7 {
		t.Fatal("invalid frozen synthetic v7 evidence", err)
	}
	for _, sample := range fixture.Cases {
		if sample.ExpectedAllow || len(sample.Decision.Messages) == 0 || len(sample.Decision.Messages) != len(sample.Decision.ReplyContexts) || len(sample.OriginReportSHA256) != 64 {
			t.Fatal("incomplete frozen candidate", sample.ID)
		}
	}
	return fixture.Cases, raw
}

// The approved replay translates all timestamps by a single delta. This keeps
// relative gaps (including the actual 58 seconds behind the false two-minute
// statement) and every literal body/participant unchanged. The original frozen
// input and mapping remain in the report; no provider input is patched.
func deepV7Replay(t *testing.T, store *Store, c Config, sample deepV7FrozenCandidate) (Input, Decision, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(sample)
	var replay deepV7FrozenCandidate
	if err := json.Unmarshal(raw, &replay); err != nil {
		t.Fatal(err)
	}
	oldAt, err := time.Parse(time.RFC3339Nano, replay.Input.MessageTime)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	delta := now.Sub(oldAt)
	ids := map[string]string{}
	shift := func(source *SourceRef) {
		if source == nil {
			return
		}
		for _, value := range []*string{&source.OccurredAt, &source.ReceivedAt} {
			if *value == "" {
				continue
			}
			at, parseErr := time.Parse(time.RFC3339Nano, *value)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			*value = at.Add(delta).Format(time.RFC3339Nano)
		}
		source.EventID = ""
	}
	for _, turn := range replay.History {
		if turn.Role != "user" || turn.Source == nil {
			t.Fatal("replay fixture must contain authoritative user events")
		}
		oldID := turn.EventID
		shift(turn.Source)
		shift(turn.QuotedSource)
		for i := range turn.IncomingMedia {
			shift(&turn.IncomingMedia[i].Source)
		}
		literal := turn.Text
		if len(turn.IncomingMedia) != 0 {
			literal = ""
		}
		if fmt.Sprintf("%x", sha256.Sum256([]byte(literal))) != turn.Source.BodyHash {
			t.Fatal("frozen source literal/hash mismatch", sample.ID)
		}
		in := Input{Channel: replay.Input.Channel, Account: replay.Input.Account, Thread: replay.Input.Thread, Sender: turn.Sender, SenderName: turn.SenderName, Text: turn.Text, OriginalText: &literal, Source: turn.Source, QuotedSource: turn.QuotedSource, Addressees: turn.Addressees, IncomingMedia: turn.IncomingMedia, TraceID: turn.TraceID, MessageTime: turn.Time.Add(delta).Format(time.RFC3339Nano), IsGroup: true, Mode: "direct"}
		if err = store.Observe(in, c, now); err != nil {
			t.Fatal(err)
		}
		proof, ok := store.SourceForInput(in)
		if !ok {
			t.Fatal("replayed source missing")
		}
		ids[oldID] = proof.EventID
	}
	in := replay.Input
	if in.Source == nil || in.OriginalText == nil {
		t.Fatal("frozen current lacks original native source")
	}
	oldAnchor := ""
	for _, rc := range replay.Decision.ReplyContexts {
		if rc.AnchorMessageID == in.Source.MessageID {
			oldAnchor = rc.AnchorEventID
			break
		}
	}
	shift(in.Source)
	shift(in.QuotedSource)
	for i := range in.IncomingMedia {
		shift(&in.IncomingMedia[i].Source)
	}
	in.MessageTime = oldAt.Add(delta).Format(time.RFC3339Nano)
	in.TraceID = in.Source.MessageID
	if err = store.Observe(in, c, now); err != nil {
		t.Fatal(err)
	}
	proof, ok := store.SourceForInput(in)
	if !ok {
		t.Fatal("current replay event unavailable")
	}
	if oldAnchor != "" {
		ids[oldAnchor] = proof.EventID
	}
	d := replay.Decision
	for i := range d.ReplyContexts {
		rc := &d.ReplyContexts[i]
		if rc.AnchorEventID != "" {
			mapped, exists := ids[rc.AnchorEventID]
			if !exists {
				t.Fatal("unmapped replay anchor")
			}
			rc.AnchorEventID = mapped
		}
		for j, id := range rc.SourceEventIDs {
			mapped, exists := ids[id]
			if !exists {
				t.Fatal("unmapped replay source", sample.ID, id)
			}
			rc.SourceEventIDs[j] = mapped
		}
		rc.SourceVersions = nil
		rc.ContextVersion, rc.State, rc.EvidenceEpoch = "", "", ""
	}
	return in, d, map[string]any{"time_delta_nanoseconds": int64(delta), "original_current_time": oldAt.Format(time.RFC3339Nano), "replay_current_time": now.Format(time.RFC3339Nano), "event_ids": ids, "method": "single delta for all source/history/current/quoted times; unchanged literal bodies, participants, media observations, full draft and gold"}
}

type deepStructuredFake struct {
	grounding int
	options   deepseek.GroundingOptions
}

func (f *deepStructuredFake) Chat(context.Context, string, []map[string]string) (string, error) {
	return "", errors.New("ordinary Chat must not be used")
}
func (f *deepStructuredFake) ChatFunctions(context.Context, string, []map[string]string, []mcptools.Tool) (string, error) {
	return "", errors.New("tools must not be used")
}
func (f *deepStructuredFake) ChatGrounding(_ context.Context, _ string, turns []map[string]string, options deepseek.GroundingOptions) (string, error) {
	f.grounding++
	f.options = options
	return syntheticGroundingVerdict(turns, "allow"), nil
}

func TestDeepRepairRecordingWrapperForwardsStructuredReview(t *testing.T) {
	mainBudget, toolBudget, budgetErr := deepRepairGenerationBudgets(768, 4096)
	if budgetErr != nil || mainBudget != 768 || toolBudget != 4096 {
		t.Fatal("runtime generation profile was not preserved")
	}
	if _, _, err := deepRepairGenerationBudgets(5000, 4096); err == nil {
		t.Fatal("actual provider limit must be reported, not silently clamped")
	}
	for _, raw := range []string{`{}`, `{"grounding":{"max_tokens":8192,"timeout_seconds":120,"reasoning_effort":"high"}}`, `{"review_model":"deepseek-v4-pro"}`, `{"review_model":"deepseek-v4-pro","grounding":{"model":"deepseek-v4-pro","max_tokens":8192,"timeout_seconds":120,"reasoning_effort":"high"}}`, `{"review_timeout_seconds":180}`} {
		if err := deepRepairFrozenReviewConfig([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{`{"grounding":{"timeout_seconds":180}}`, `{"review_timeout_seconds":-1}`, `{"grounding":{"timeout":180}}`, `{"grounding":{"model":"deepseek-v4-pro"}}`, `{"review_model":"bad model"}`, `{"review_max_tokens":393217}`, `{"review_reasoning_effort":"unsupported"}`} {
		if err := deepRepairFrozenReviewConfig([]byte(raw)); err == nil {
			t.Fatal("explicit override silently ignored", raw)
		}
	}
	longer := Config{Grounding: &GroundingSettings{TimeoutSeconds: 180}}
	if deepRepairOuterDeadline(longer, 1) != 190*time.Second || deepRepairOuterDeadline(longer, 3) != 660*time.Second {
		t.Fatal("outer deadline clamped effective timeout")
	}
	fake := &deepStructuredFake{}
	recording := &deepRepairRecordingModel{client: fake, chatModel: "deepseek-flash"}
	options := deepseek.GroundingOptions{Model: "deepseek-v4-pro", MaxTokens: 16384, Timeout: 180 * time.Second, ReasoningEffort: "max"}
	_, err := recording.ChatGrounding(t.Context(), "synthetic JSON policy", []map[string]string{{"role": "user", "content": "synthetic evidence"}}, options)
	if err != nil || fake.grounding != 1 || !reflect.DeepEqual(fake.options, options) || len(recording.responses) != 1 || recording.responses[0]["stage"] != "grounding" {
		t.Fatal("structured review interface was not forwarded exactly", err)
	}
	var actual deepseek.GroundingOptions
	if json.Unmarshal([]byte(recording.responses[0]["effective_grounding_options"]), &actual) != nil || !reflect.DeepEqual(actual, options) {
		t.Fatal("effective options not recorded")
	}
	if recording.responses[0]["actual_review_model"] != "deepseek-v4-pro" {
		t.Fatal("actual reviewer was misreported as chat model")
	}
	config := deepRepairModelConfig("https://synthetic.invalid", "configured-model", false)
	review := config["grounding"].(map[string]any)
	if review["max_tokens"] != 8192 || review["timeout_seconds"] != float64(120) || review["thinking"] != "enabled" || review["reasoning_effort"] != "high" || review["response_format"] != "json_object" {
		t.Fatal("effective review configuration missing from fingerprint")
	}
	explicit := deepRepairModelConfig("https://synthetic.invalid", "configured-model", false, "deepseek-v4-pro")
	if explicit["model"] != "configured-model" || explicit["actual_review_model"] != "deepseek-v4-pro" || explicit["grounding"].(map[string]any)["model"] != "deepseek-v4-pro" || explicit["review_protocol"] == config["review_protocol"] {
		t.Fatal("default and explicit-review protocols not separated")
	}
	defaultBytes, _ := json.Marshal(config)
	explicitBytes, _ := json.Marshal(explicit)
	if sha256.Sum256(defaultBytes) == sha256.Sum256(explicitBytes) {
		t.Fatal("review model missing from configuration fingerprint")
	}
	runtimeProfile := deepRepairModelConfigWithBudgets("https://synthetic.invalid", "deepseek-flash", false, "deepseek-v4-pro", mainBudget, toolBudget)
	if runtimeProfile["max_tokens"] != 768 || runtimeProfile["tool_max_tokens"] != 4096 || runtimeProfile["grounding"].(map[string]any)["max_tokens"] != 8192 {
		t.Fatal("generation/review profile conflated in evidence")
	}
	for _, nested := range []bool{false, true} {
		t.Run(fmt.Sprintf("production-checker-nested-%v", nested), func(t *testing.T) {
			store, c, _ := memoryFixture(t)
			c.Enabled, c.Permissions.ReplyText = true, true
			deepRepairApplyReviewModel(&c, "deepseek-v4-pro")
			in, rc := deepVerifierScene(t, store, c, "greeting")
			fake := &deepStructuredFake{}
			recording := &deepRepairRecordingModel{client: fake, chatModel: "deepseek-flash", captureGroundingRequests: true}
			var model LanguageModel = recording
			if nested {
				model = &deepSourceRoleRecordingModel{deepRepairRecordingModel: recording}
			}
			engine, err := New(c, store, model, "synthetic")
			if err != nil {
				t.Fatal(err)
			}
			draft, err := engine.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{"你好。"}, ReplyContexts: []ReplyContext{rc}})
			if err != nil {
				t.Fatal(err)
			}
			verdict, err := engine.checkReplyGrounding(t.Context(), in, draft)
			if err != nil || verdict.Verdict != "allow" || len(verdict.CandidateID) != 64 || fake.grounding != 1 || fake.options.Model != "deepseek-v4-pro" || recording.responses[0]["actual_review_model"] != "deepseek-v4-pro" || fake.options.MaxTokens != 8192 || fake.options.Timeout != 120*time.Second || fake.options.ReasoningEffort != "high" {
				t.Fatal("production checker lost structured route or host digest binding", err, verdict, fake.options)
			}
			var request struct {
				Messages []map[string]string `json:"messages"`
			}
			if json.Unmarshal([]byte(recording.responses[0]["synthetic_request"]), &request) != nil || len(request.Messages) != 1 {
				t.Fatal("missing synthetic review request")
			}
			var state struct {
				Settings  GroundingSettings `json:"review_settings"`
				Transport string            `json:"review_transport"`
			}
			if json.Unmarshal(verdict.ReviewBinding.OriginalState, &state) != nil || state.Settings.Model != "deepseek-v4-pro" || state.Transport != "dedicated_structured_review" {
				t.Fatal("review evidence metadata did not record actual reviewer", state)
			}
		})
	}
}

func TestDeepRepairExplicitV11ReviewProfileForwarding(t *testing.T) {
	raw := []byte(`{"review_model":"deepseek-v4-pro","review_max_tokens":32768,"review_timeout_seconds":240,"review_reasoning_effort":"high"}`)
	profile, err := deepRepairDeclaredReviewProfile(raw)
	if err != nil || profile.Model != "deepseek-v4-pro" || profile.MaxTokens != 32768 || profile.TimeoutSeconds != 240 || profile.ReasoningEffort != "high" {
		t.Fatal("declared review profile not consumed", err, profile)
	}
	store, c, _ := memoryFixture(t)
	c.Enabled, c.Permissions.ReplyText = true, true
	deepRepairApplyReviewProfile(&c, profile)
	if deepRepairOuterDeadline(c, 1) != 250*time.Second || deepRepairOuterDeadline(c, 3) != 840*time.Second {
		t.Fatal("review profile was truncated by outer deadline")
	}
	in, rc := deepVerifierScene(t, store, c, "greeting")
	fake := &deepStructuredFake{}
	recording := &deepRepairRecordingModel{client: fake, chatModel: "deepseek-flash", captureGroundingRequests: true}
	engine, err := New(c, store, recording, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	draft, err := engine.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{"你好。"}, ReplyContexts: []ReplyContext{rc}})
	if err != nil {
		t.Fatal(err)
	}
	verdict, err := engine.checkReplyGrounding(t.Context(), in, draft)
	if err != nil || verdict.Verdict != "allow" || fake.options.Model != "deepseek-v4-pro" || fake.options.MaxTokens != 32768 || fake.options.Timeout != 240*time.Second || fake.options.ReasoningEffort != "high" {
		t.Fatal("review profile was ignored or clamped", err, fake.options)
	}
	actual := deepRepairModelConfigWithBudgets("https://synthetic.invalid", "deepseek-flash", false, profile.Model, 768, 4096, profile)
	grounding := actual["grounding"].(map[string]any)
	if actual["review_protocol"] != "structured-review-explicit-profile-v11" || grounding["max_tokens"] != 32768 || grounding["timeout_seconds"] != 240 {
		t.Fatal("explicit profile missing from fingerprint")
	}
	previous := deepRepairModelConfigWithBudgets("https://synthetic.invalid", "deepseek-flash", false, profile.Model, 768, 4096)
	a, _ := json.Marshal(actual)
	b, _ := json.Marshal(previous)
	if sha256.Sum256(a) == sha256.Sum256(b) {
		t.Fatal("different review profiles share a configuration hash")
	}
}

func TestDeepRepairV7RegressionSyntheticPreparation(t *testing.T) {
	samples, _ := deepV7FrozenCandidates(t)
	for _, sample := range samples {
		t.Run(sample.ID, func(t *testing.T) {
			store, c, _ := memoryFixture(t)
			c.Enabled, c.Permissions.ReplyText = true, true
			in, d, mapping := deepV7Replay(t, store, c, sample)
			engine, err := New(c, store, extractionFixture(func(context.Context, string, []map[string]string) (string, error) {
				return "", errors.New("no model during preparation")
			}), "synthetic")
			if err != nil {
				t.Fatal(err)
			}
			normalized, err := engine.normalizeReplyContexts(t.Context(), in, d)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(normalized.Messages, sample.Decision.Messages) || mapping["event_ids"] == nil {
				t.Fatal("replay changed frozen draft")
			}
			for _, rc := range normalized.ReplyContexts {
				if err = engine.ValidateReplyContext(t.Context(), in, rc); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestDeepRepairV7RegressionRealModel(t *testing.T) {
	configFile := os.Getenv("WECHAT_DEEP_REPAIR_MODEL_CONFIG")
	if configFile == "" {
		t.Skip("opt-in fixed synthetic v7 failures")
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
		t.Fatal("invalid explicit model config")
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
	if err != nil || json.Unmarshal(raw, &manifest) != nil || len(manifest.SHA256) != 64 || manifest.Driver == "" {
		t.Fatal("source manifest required")
	}
	samples, fixture := deepV7FrozenCandidates(t)
	protocol := sha256.New()
	for _, name := range []string{"deep_repair_v7_regression_live_test.go", "deep_repair_acceptance_test.go", "deep_repair_provider_block_test.go"} {
		body, readErr := os.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		protocol.Write([]byte(name + "\x00"))
		protocol.Write(body)
	}
	protocolHash := fmt.Sprintf("%x", protocol.Sum(nil))
	fixtureHash := fmt.Sprintf("%x", sha256.Sum256(fixture))
	modelConfig := deepRepairModelConfigWithBudgets(cfg.BaseURL, cfg.Model, cfg.Compatible, cfg.ReviewModel, cfg.MaxTokens, cfg.ToolMaxTokens, reviewProfile)
	configBytes, _ := json.Marshal(modelConfig)
	configHash := fmt.Sprintf("%x", sha256.Sum256(configBytes))
	usage := []deepseek.Usage{}
	provider, err := deepseek.New(deepseek.Config{BaseURL: cfg.BaseURL, Model: cfg.Model, KeyFile: cfg.KeyFile, Compatible: cfg.Compatible, MaxTokens: cfg.MaxTokens, ToolMaxTokens: cfg.ToolMaxTokens, OnUsage: func(u deepseek.Usage) { usage = append(usage, u) }})
	if err != nil {
		t.Fatal("model initialization failed")
	}
	client := deepRepairGuardProvider(provider)
	type row struct {
		ID            string                `json:"id"`
		ExpectedAllow bool                  `json:"expected_allow"`
		Frozen        deepV7FrozenCandidate `json:"frozen_candidate"`
		Input         Input                 `json:"synthetic_input"`
		Evidence      Scope                 `json:"synthetic_source_evidence"`
		Candidate     Decision              `json:"replayed_candidate"`
		Mapping       map[string]any        `json:"replay_mapping"`
		Check         GroundingVerdict      `json:"check_verdict"`
		Calls         []map[string]string   `json:"synthetic_model_responses"`
		Usage         []deepseek.Usage      `json:"usage"`
		ElapsedMS     float64               `json:"elapsed_ms"`
		Pass          bool                  `json:"pass"`
		Unchanged     bool                  `json:"draft_unchanged"`
		Error         string                `json:"error,omitempty"`
	}
	results := []row{}
	destination := os.Getenv("WECHAT_DEEP_REPAIR_V7_REGRESSION_REPORT")
	if destination == "" {
		t.Fatal("report destination required")
	}
	completed := map[string]bool{}
	if previous, readErr := os.ReadFile(destination); readErr == nil {
		if os.Getenv("WECHAT_DEEP_REPAIR_V7_REGRESSION_RESUME") != "1" {
			t.Fatal("report exists; select new path or explicitly resume missing cases only")
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
			t.Fatal("checkpoint identity changed; select a new report")
		}
		results, usage = checkpoint.Results, checkpoint.Usage
		for _, r := range results {
			if completed[r.ID] {
				t.Fatal("duplicate checkpoint case")
			}
			completed[r.ID] = true
		}
	}
	plannedCaseIDs := []string{}
	for _, item := range samples {
		plannedCaseIDs = append(plannedCaseIDs, item.ID)
	}
	write := func(partial bool) {
		attemptedCaseIDs := []string{}
		for _, item := range results {
			attemptedCaseIDs = append(attemptedCaseIDs, item.ID)
		}
		passed := 0
		for _, r := range results {
			if r.Pass {
				passed++
			}
		}
		report := map[string]any{"version": "deep-repair-v7-fixed-regression-v1", "production_sha256": manifest.SHA256, "production_hash_method": manifest.Method, "protocol_sha256": protocolHash, "fixture_sha256": fixtureHash, "config_sha256": configHash, "driver_sha256": manifest.Driver, "model_config": modelConfig, "cases": len(results), "requested_cases": 7, "bad_cases": len(results), "bad_rejected": passed, "good_cases": 0, "good_accepted": 0, "passed": !partial && passed == 7, "partial": partial, "synthetic_only": true, "editor_calls": 0, "generation_calls": 0, "wechat_sends": 0, "results": results, "usage": usage, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "method": "seven frozen full v7 candidates with complete synthetic source context; every bad draft must explicitly reject; no editor/regeneration; original 37 positive-control threshold remains independent"}
		client.Progress(report, plannedCaseIDs, attemptedCaseIDs)
		out, e := json.MarshalIndent(report, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.MkdirAll(filepath.Dir(destination), 0700); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(destination+".pending", out, 0600); e != nil {
			t.Fatal(e)
		}
		if e = os.Rename(destination+".pending", destination); e != nil {
			t.Fatal(e)
		}
	}
	for _, sample := range samples {
		if completed[sample.ID] {
			continue
		}
		store, c, _ := memoryFixture(t)
		deepRepairApplyReviewProfile(&c, reviewProfile)
		c.Enabled, c.Permissions.ReplyText = true, true
		c.Permissions.ObserveGroups, c.Permissions.AutonomousGroups = true, true
		c.QuietStart, c.QuietEnd = "00:00", "00:00"
		in, d, mapping := deepV7Replay(t, store, c, sample)
		recording := &deepRepairRecordingModel{client: client, chatModel: cfg.Model, captureGroundingRequests: true}
		engine, newErr := New(c, store, recording, "依据来源自然回复。")
		if newErr != nil {
			t.Fatal(newErr)
		}
		d, err = engine.normalizeReplyContexts(t.Context(), in, d)
		if err != nil {
			t.Fatal(err)
		}
		before, _ := json.Marshal(d)
		start := time.Now()
		usageStart := len(usage)
		ctx, cancel := context.WithTimeout(t.Context(), deepRepairOuterDeadline(engine.Config, 1))
		client.Begin(sample.ID, "fixed_review")
		verdict, checkErr := engine.checkReplyGrounding(ctx, in, d)
		cancel()
		after, _ := json.Marshal(d)
		r := row{ID: sample.ID, Frozen: sample, Input: in, Evidence: store.Context(in, c, time.Now()), Candidate: d, Mapping: mapping, Check: verdict, Calls: recording.responses, Usage: append([]deepseek.Usage(nil), usage[usageStart:]...), ElapsedMS: float64(time.Since(start)) / float64(time.Millisecond), Unchanged: string(before) == string(after)}
		if checkErr != nil {
			r.Error = checkErr.Error()
			if diagnostic, ok := deepseek.ErrorDiagnostics(checkErr); ok {
				r.Error = fmt.Sprintf("%s stage=%s http=%d attempts=%d", diagnostic.Code, diagnostic.Stage, diagnostic.HTTPStatus, diagnostic.Attempts)
			}
		}
		r.Pass = checkErr == nil && r.Unchanged && verdict.Verdict == "reject"
		results = append(results, r)
		write(true)
		_ = store.Close()
		if client.Block() != nil {
			t.Fatalf("provider blocked; partial checkpoint preserved: %s", client.FailureText())
		}
	}
	write(false)
	for _, r := range results {
		if !r.Pass {
			t.Errorf("frozen failure not rejected: %s verdict=%s error=%s", r.ID, r.Check.Verdict, r.Error)
		}
	}
}
