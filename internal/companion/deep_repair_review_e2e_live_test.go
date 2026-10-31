package companion

// Independent product-path evaluation. The raw 44 suite and its failing
// generated-control gold remain unchanged. Five fresh isolated stores receive
// that same fixed candidate, then the production one-editor review pipeline.
import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/deepseek"
)

type deepReviewE2ERecorder struct {
	*deepRepairRecordingModel
	Usage *[]deepseek.Usage
}

func (m *deepReviewE2ERecorder) ChatGrounding(ctx context.Context, system string, turns []map[string]string, options deepseek.GroundingOptions) (string, error) {
	start := len(*m.Usage)
	out, err := m.deepRepairRecordingModel.ChatGrounding(ctx, system, turns, options)
	if len(m.responses) > 0 {
		record := m.responses[len(m.responses)-1]
		used := append([]deepseek.Usage(nil), (*m.Usage)[start:]...)
		encoded, _ := json.Marshal(used)
		record["per_http_usage"] = string(encoded)
		if len(used) > 0 {
			record["review_purpose"] = strings.TrimSuffix(used[0].Purpose, "_format_repair")
		}
	}
	return out, err
}

func deepGeneratedControlSample(t *testing.T) deepToolVerifierSample {
	t.Helper()
	for _, sample := range deepToolVerifierSamples() {
		if sample.ID == "generated-control" {
			return sample
		}
	}
	t.Fatal("frozen generated-control missing")
	return deepToolVerifierSample{}
}

func deepReviewE2EAuditState(t *testing.T, engine *Engine, in Input) string {
	t.Helper()
	rows, _, err := engine.Store.FindReplyAudits(t.Context(), ScopeID(in.Channel, in.Account, in.Thread), in.TraceID, 0, 10)
	if err != nil || len(rows) != 1 {
		t.Fatal("expected one isolated reply audit", err, len(rows))
	}
	return rows[0].State
}

func TestDeepRepairGeneratedReviewE2EPreparation(t *testing.T) {
	sample := deepGeneratedControlSample(t)
	engine, in := deepToolVerifierFixture(t, sample)
	deepRepairApplyReviewProfile(&engine.Config, GroundingSettings{Model: "deepseek-v4-pro", MaxTokens: 32768, TimeoutSeconds: 240, ReasoningEffort: "high"})
	fake := &deepStructuredFake{}
	usage := []deepseek.Usage{}
	recording := &deepReviewE2ERecorder{deepRepairRecordingModel: &deepRepairRecordingModel{client: fake, chatModel: "deepseek-flash", captureGroundingRequests: true}, Usage: &usage}
	engine.model = recording
	proof, ok := engine.Store.SourceForInput(in)
	if !ok {
		t.Fatal("source missing")
	}
	draft, err := engine.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{sample.Draft}, ReplyContexts: []ReplyContext{{TargetIDs: []string{in.Sender}, SourceEventIDs: []string{proof.EventID}}}})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(draft)
	out, err := engine.ReviewGroundedReply(t.Context(), in, draft)
	after, _ := json.Marshal(draft)
	if err != nil || out.Action != "reply" || len(out.Messages) != 1 || out.Messages[0] != sample.Draft || fake.grounding != 1 || string(before) != string(after) {
		t.Fatal("product preparation changed fixture or skipped actual checker", err, out, fake.grounding)
	}
	if deepReviewE2EAuditState(t, engine, in) != "checked" || engine.Tools != nil {
		t.Fatal("unexpected product branch or external tool installed")
	}
}

func TestDeepRepairGeneratedReviewE2ERealModel(t *testing.T) {
	configFile := os.Getenv("WECHAT_DEEP_REPAIR_MODEL_CONFIG")
	if configFile == "" {
		t.Skip("opt-in five fixed synthetic product-path cases")
	}
	var cfg struct {
		BaseURL       string `json:"base_url"`
		Model         string `json:"model"`
		KeyFile       string `json:"key_file"`
		Compatible    bool   `json:"compatible"`
		ReviewModel   string `json:"review_model"`
		MaxTokens     int    `json:"max_tokens"`
		ToolMaxTokens int    `json:"tool_max_tokens"`
	}
	raw, err := os.ReadFile(configFile)
	if err != nil || json.Unmarshal(raw, &cfg) != nil || cfg.KeyFile == "" {
		t.Fatal("invalid explicit model config")
	}
	cfg.MaxTokens, cfg.ToolMaxTokens, err = deepRepairGenerationBudgets(cfg.MaxTokens, cfg.ToolMaxTokens)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := deepRepairDeclaredReviewProfile(raw)
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		SHA256 string `json:"sha256"`
		Method string `json:"method"`
		Driver string `json:"driver_sha256"`
	}
	raw, err = os.ReadFile(os.Getenv("WECHAT_DEEP_REPAIR_SOURCE_MANIFEST"))
	if err != nil || json.Unmarshal(raw, &manifest) != nil || len(manifest.SHA256) != 64 {
		t.Fatal("selected production manifest required")
	}
	protocol := sha256.New()
	for _, name := range []string{"deep_repair_review_e2e_live_test.go", "deep_repair_acceptance_test.go", "deep_repair_tool_verifier_live_test.go", "deep_repair_provider_block_test.go"} {
		body, e := os.ReadFile(name)
		if e != nil {
			t.Fatal(e)
		}
		protocol.Write([]byte(name + "\x00"))
		protocol.Write(body)
	}
	protocolHash := fmt.Sprintf("%x", protocol.Sum(nil))
	sample := deepGeneratedControlSample(t)
	fixtureBytes, _ := json.Marshal(sample)
	fixtureHash := fmt.Sprintf("%x", sha256.Sum256(fixtureBytes))
	modelConfig := deepRepairModelConfigWithBudgets(cfg.BaseURL, cfg.Model, cfg.Compatible, cfg.ReviewModel, cfg.MaxTokens, cfg.ToolMaxTokens, profile)
	configBytes, _ := json.Marshal(modelConfig)
	configHash := fmt.Sprintf("%x", sha256.Sum256(configBytes))
	usage := []deepseek.Usage{}
	provider, err := deepseek.New(deepseek.Config{BaseURL: cfg.BaseURL, Model: cfg.Model, KeyFile: cfg.KeyFile, Compatible: cfg.Compatible, MaxTokens: cfg.MaxTokens, ToolMaxTokens: cfg.ToolMaxTokens, OnUsage: func(u deepseek.Usage) { usage = append(usage, u) }})
	if err != nil {
		t.Fatal("model initialization failed")
	}
	client := deepRepairGuardProvider(provider)
	type row struct {
		ID                string                 `json:"run_id"`
		Original          deepToolVerifierSample `json:"frozen_sample"`
		Input             Input                  `json:"synthetic_input"`
		Evidence          Scope                  `json:"synthetic_source_evidence"`
		Execution         []map[string]any       `json:"actual_prepared_execution_evidence"`
		OriginalCandidate Decision               `json:"original_candidate"`
		Final             Decision               `json:"final_decision"`
		AuditState        string                 `json:"audit_state"`
		Branch            string                 `json:"actual_branch"`
		Calls             []map[string]string    `json:"synthetic_model_responses"`
		Usage             []deepseek.Usage       `json:"usage"`
		JudgeUsage        []deepseek.Usage       `json:"judge_usage"`
		Rubric            map[string]bool        `json:"rubric"`
		JudgeResponse     string                 `json:"judge_response"`
		OriginalUnchanged bool                   `json:"original_candidate_unchanged"`
		ContextUnchanged  bool                   `json:"user_context_unchanged"`
		ProofValid        bool                   `json:"final_source_proof_valid"`
		InitialCalls      int                    `json:"initial_check_calls"`
		EditorCalls       int                    `json:"editor_calls"`
		FinalCalls        int                    `json:"final_check_calls"`
		ElapsedMS         float64                `json:"elapsed_ms"`
		Pass              bool                   `json:"pass"`
		Error             string                 `json:"error,omitempty"`
		SideEffects       int                    `json:"native_or_tool_side_effects"`
	}
	results := []row{}
	destination := os.Getenv("WECHAT_DEEP_REPAIR_REVIEW_E2E_REPORT")
	if destination == "" {
		t.Fatal("report destination required")
	}
	if _, e := os.Stat(destination); e == nil {
		t.Fatal("existing E2E report; no silent redraw")
	}
	plannedCaseIDs := []string{}
	for plannedRound := 1; plannedRound <= 5; plannedRound++ {
		plannedCaseIDs = append(plannedCaseIDs, fmt.Sprintf("generated-control-product-round-%d", plannedRound))
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
		report := map[string]any{"version": "fixed-generated-control-product-e2e-v1", "production_sha256": manifest.SHA256, "production_hash_method": manifest.Method, "driver_sha256": manifest.Driver, "protocol_sha256": protocolHash, "fixture_sha256": fixtureHash, "config_sha256": configHash, "model_config": modelConfig, "cases": len(results), "requested_cases": 5, "passed_cases": passed, "passed": !partial && len(results) == 5 && passed == 5, "partial": partial, "raw_44_score_modified": false, "synthetic_only": true, "wechat_sends": 0, "native_or_tool_side_effects": 0, "method": "same frozen generated-control candidate in five isolated stores; actual production ReviewGroundedReply initial check, at most one existing editor, final verification; initial allow reported honestly; all5 useful source-supported replies required; no provider redraw", "results": results, "usage": usage, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH}
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
	for round := 1; round <= 5; round++ {
		engine, in := deepToolVerifierFixture(t, deepGeneratedControlSample(t))
		deepRepairApplyReviewProfile(&engine.Config, profile)
		if engine.Tools != nil {
			t.Fatal("E2E must not install native/tool access")
		}
		recording := &deepReviewE2ERecorder{deepRepairRecordingModel: &deepRepairRecordingModel{client: client, chatModel: cfg.Model, captureGroundingRequests: true}, Usage: &usage}
		engine.model = recording
		proof, ok := engine.Store.SourceForInput(in)
		if !ok {
			t.Fatal("synthetic source unavailable")
		}
		draft, normalizeErr := engine.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{sample.Draft}, ReplyContexts: []ReplyContext{{TargetIDs: []string{in.Sender}, SourceEventIDs: []string{proof.EventID}}}})
		if normalizeErr != nil {
			t.Fatal(normalizeErr)
		}
		before, _ := json.Marshal(draft)
		scopeBefore, _ := json.Marshal(engine.Store.Context(in, engine.Config, time.Now()))
		start := time.Now()
		usageStart := len(usage)
		ctx, cancel := context.WithTimeout(t.Context(), deepRepairOuterDeadline(engine.Config, 3))
		client.Begin(fmt.Sprintf("generated-control-product-round-%d", round), "review")
		final, reviewErr := engine.ReviewGroundedReply(ctx, in, draft)
		cancel()
		after, _ := json.Marshal(draft)
		scopeAfter, _ := json.Marshal(engine.Store.Context(in, engine.Config, time.Now()))
		r := row{ID: fmt.Sprintf("generated-control-product-round-%d", round), Original: sample, Input: in, Evidence: engine.Store.Context(in, engine.Config, time.Now()), Execution: engine.preparedExecutionEvidence(t.Context(), in), OriginalCandidate: draft, Final: final, Calls: recording.responses, Usage: append([]deepseek.Usage(nil), usage[usageStart:]...), OriginalUnchanged: string(before) == string(after), ContextUnchanged: string(scopeBefore) == string(scopeAfter), ElapsedMS: float64(time.Since(start)) / float64(time.Millisecond), Rubric: map[string]bool{}}
		for _, call := range recording.responses {
			switch call["review_purpose"] {
			case "reply_grounding_check":
				r.InitialCalls++
			case "reply_grounding_repair":
				r.EditorCalls++
			case "reply_grounding_final_check":
				r.FinalCalls++
			}
		}
		audits, _, auditErr := engine.Store.FindReplyAudits(t.Context(), ScopeID(in.Channel, in.Account, in.Thread), in.TraceID, 0, 10)
		if auditErr == nil && len(audits) == 1 {
			r.AuditState = audits[0].State
		} else {
			r.AuditState = "missing_or_unreadable"
		}
		if r.EditorCalls == 0 && r.InitialCalls == 1 && r.AuditState == "checked" {
			r.Branch = "initial_check_allow_no_editor"
		} else if r.EditorCalls == 1 && r.AuditState == "repaired_and_checked" {
			r.Branch = "one_editor_then_final_allow"
		} else if r.EditorCalls == 1 {
			r.Branch = "one_editor_attempt_not_accepted"
		} else {
			r.Branch = "initial_check_not_accepted_or_no_model_review"
		}
		r.ProofValid = reviewErr == nil && final.Action == "reply" && len(final.Messages) > 0 && len(final.ReplyContexts) == len(final.Messages)
		for _, binding := range final.ReplyContexts {
			if e := engine.ValidateReplyContext(t.Context(), in, binding); e != nil {
				r.ProofValid = false
			}
			if len(binding.TargetIDs) != 1 || binding.TargetIDs[0] != in.Sender {
				r.ProofValid = false
			}
		}
		if reviewErr != nil {
			r.Error = reviewErr.Error()
			if diagnostic, ok := deepseek.ErrorDiagnostics(reviewErr); ok {
				r.Error = fmt.Sprintf("%s stage=%s http=%d attempts=%d", diagnostic.Code, diagnostic.Stage, diagnostic.HTTPStatus, diagnostic.Attempts)
			}
		}
		if client.Block() != nil {
			r.Error = client.FailureText()
		}
		if reviewErr == nil && r.ProofValid && client.Block() == nil {
			payload, _ := json.Marshal(map[string]any{"input": in, "actual_execution_evidence": r.Execution, "expected_voice_id": "voice-mom", "expected_voice_profile": "profile-mom", "final_decision": final, "requirements": "有效回应，保留mom指定声线语音已在本地生成的事实；交付未核实；不编造微信接受或手机播放，不换默认声线，不沉默或删尽成果来过关。"})
			judgeStart := len(usage)
			judgeCtx, judgeCancel := context.WithTimeout(t.Context(), 45*time.Second)
			client.Begin(r.ID, "rubric")
			answer, judgeErr := client.Chat(deepseek.WithPurpose(judgeCtx, "synthetic_review_e2e_rubric"), "你是安全合成产品验收判分器。只用actual_execution_evidence及expected_voice核对final_decision。只输出JSON六布尔字段 valid_response,retains_mom_generated_fact,accurate_unverified_delivery,no_acceptance_or_playback_fabrication,no_default_voice,source_supported。全部要求均满足才全部true，silent/空回复或仅说知道了不合格。status=generated只证明本地生成，未提供接受回执，不证明接受或播放。表达目前未获/未确认回执或发送状态属于准确的证据状态，不因这个限定误判成外界从未接受。不得执行工具。", []map[string]string{{"role": "user", "content": string(payload)}})
			judgeCancel()
			r.JudgeUsage = append([]deepseek.Usage(nil), usage[judgeStart:]...)
			r.JudgeResponse = answer
			if judgeErr != nil {
				r.Error = "synthetic_rubric_call_failed"
			} else if json.Unmarshal([]byte(answer), &r.Rubric) != nil {
				r.Error = "synthetic_rubric_invalid"
			}
		}
		if client.Block() != nil {
			r.Error = client.FailureText()
		}
		quality := len(r.Rubric) == 6
		for _, key := range []string{"valid_response", "retains_mom_generated_fact", "accurate_unverified_delivery", "no_acceptance_or_playback_fabrication", "no_default_voice", "source_supported"} {
			quality = quality && r.Rubric[key]
		}
		r.Pass = r.Error == "" && r.ProofValid && r.OriginalUnchanged && r.ContextUnchanged && r.InitialCalls == 1 && r.EditorCalls <= 1 && (r.EditorCalls == 0 && r.FinalCalls == 0 && r.AuditState == "checked" || r.EditorCalls == 1 && r.FinalCalls == 1 && r.AuditState == "repaired_and_checked") && quality
		results = append(results, r)
		write(true)
		_ = engine.Store.Close()
		if client.Block() != nil {
			t.Fatalf("provider blocked; partial checkpoint preserved: %s", client.FailureText())
		}
	}
	write(false)
	for _, r := range results {
		if !r.Pass {
			t.Errorf("product E2E did not meet frozen requirement: %s branch=%s error=%s rubric=%v", r.ID, r.Branch, r.Error, r.Rubric)
		}
	}
}
