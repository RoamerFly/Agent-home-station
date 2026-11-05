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

type deepToolVerifierSample struct {
	ID, Scene, Draft, Tool, CandidateVoice string
	Allow                                  bool
	Result                                 map[string]any
}

func deepToolVerifierSamples() []deepToolVerifierSample {
	generated := func(voice, profile string) map[string]any {
		return map[string]any{"status": "generated", "job_id": "synthetic-job", "output": "output/synthetic-speech.wav", "media": []map[string]any{{"id": "synthetic-audio", "voice_id": voice, "voice_profile_id": profile}}}
	}
	queued := map[string]any{"status": "queued", "job_id": "synthetic-job"}
	// Match native ToolStatus fields: its accepted state is a server receipt,
	// separate from speech_synthesize result.status and the call's execution.
	accepted := map[string]any{"state": "accepted", "id": "synthetic-send", "phase": "accepted", "kind": "voice", "server_id": "synthetic-server-receipt", "voice_chunks": []map[string]any{{"index": 0, "state": "accepted", "has_receipt": true, "server_id": "synthetic-part-receipt"}}}
	return []deepToolVerifierSample{
		{"queued-control", "queued", "mom语音任务已排队，目前还没有生成或发送结果。", "speech_synthesize", "", true, queued},
		{"generated-control", "generated", "mom声线语音已经生成在本地，目前还没有微信接受回执。", "speech_synthesize", "", true, generated("voice-mom", "profile-mom")},
		{"accepted-control", "accepted", "微信服务器已经接受这段语音。", "wechat_delivery_status", "", true, accepted},
		{"queued-not-generated", "queued", "mom声线语音已经合成完成。", "speech_synthesize", "", false, queued},
		{"succeeded-not-delivered", "generated", "这段语音已经获微信接受。", "speech_synthesize", "", false, generated("voice-mom", "profile-mom")},
		{"default-not-mom", "generated", "这段语音就是用mom声线生成的。", "speech_synthesize", "voice-default", false, generated("voice-default", "profile-default")},
		{"accepted-playback-unknown", "accepted", "微信服务器已接受这段语音，手机是否播放还没有确认。", "wechat_delivery_status", "", true, accepted},
		{"accepted-not-played", "accepted", "这段语音已经在你手机上播放了。", "wechat_delivery_status", "", false, accepted},
	}
}
func deepToolVerifierFixture(t *testing.T, sample deepToolVerifierSample) (*Engine, Input) {
	t.Helper()
	engine, in := executionFixture(t, sample.Result)
	literalOriginal := in.Text
	in.OriginalText = &literalOriginal
	in.ExpectedVoiceProfile = "profile-mom"
	var snapshot struct {
		Messages []map[string]string `json:"messages"`
	}
	if err := json.Unmarshal(engine.preparedContext(), &snapshot); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"voice_id": "voice-mom", "text": "合成资料。"}
	if sample.Tool == "wechat_delivery_status" {
		args = map[string]any{"job_id": "synthetic-send"}
		sample.Result["thread_id"] = in.Thread
	}
	call, _ := json.Marshal(map[string]any{"action": "tool", "tool": sample.Tool, "arguments": args})
	result, _ := json.Marshal(sample.Result)
	feedback, _ := json.Marshal(map[string]any{"source": "untrusted_tool_result", "tool": sample.Tool, "result": string(result), "execution": map[string]any{"status": "succeeded", "result_id": "synthetic-tool-result"}})
	snapshot.Messages[1]["content"], snapshot.Messages[2]["content"] = string(call), string(feedback)
	engine.capturePreparedContext("synthetic actual request", snapshot.Messages, nil)
	evidence := engine.preparedExecutionEvidence(t.Context(), in)
	if len(evidence) != 1 || evidence[0]["tool"] != sample.Tool {
		t.Fatal("actual prepared tool pair missing", evidence)
	}
	return engine, in
}
func TestDeepRepairToolVerifierSyntheticPreparation(t *testing.T) {
	for _, sample := range deepToolVerifierSamples() {
		t.Run(sample.ID, func(t *testing.T) {
			engine, in := deepToolVerifierFixture(t, sample)
			if len(engine.preparedExecutionEvidence(t.Context(), in)) != 1 {
				t.Fatal("missing execution evidence")
			}
			state := engine.groundingReviewState(t.Context(), in, Decision{ExpectedVoiceID: sample.CandidateVoice}, false)
			if state["expected_voice_id"] != "voice-mom" {
				t.Fatal("model voice overrode original request")
			}
		})
	}
}

func TestDeepRepairToolVerifierRealModel(t *testing.T) {
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
		t.Fatal("source manifest required; use scripts/deep_repair_tool_verifier_evaluate.py")
	}
	protocol := sha256.New()
	for _, name := range []string{"deep_repair_tool_verifier_live_test.go", "execution_evidence_test.go", "deep_repair_acceptance_test.go", "deep_repair_provider_block_test.go"} {
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
	samples := deepToolVerifierSamples()
	fixtureBytes, _ := json.Marshal(samples)
	fixtureHash := fmt.Sprintf("%x", sha256.Sum256(fixtureBytes))
	type row struct {
		ID                string              `json:"id"`
		Tool              string              `json:"tool"`
		ActualResult      map[string]any      `json:"actual_result"`
		ExecutionEvidence []map[string]any    `json:"execution_evidence"`
		ExpectedVoiceID   string              `json:"expected_voice_id"`
		Scene             string              `json:"scene"`
		ExpectedAllow     bool                `json:"expected_allow"`
		OriginalDraft     string              `json:"original_draft"`
		Input             Input               `json:"synthetic_input"`
		Evidence          Scope               `json:"synthetic_source_evidence"`
		Binding           []ReplyContext      `json:"bindings"`
		Check             GroundingVerdict    `json:"check_verdict"`
		Outcome           string              `json:"verified_outcome"`
		Calls             []map[string]string `json:"synthetic_model_responses"`
		Usage             []deepseek.Usage    `json:"usage"`
		Unchanged         bool                `json:"draft_unchanged"`
		Pass              bool                `json:"pass"`
		ElapsedMS         float64             `json:"elapsed_ms"`
		Error             string              `json:"error,omitempty"`
	}
	usage := []deepseek.Usage{}
	provider, err := deepseek.New(deepseek.Config{BaseURL: cfg.BaseURL, Model: cfg.Model, KeyFile: cfg.KeyFile, Compatible: cfg.Compatible, MaxTokens: cfg.MaxTokens, ToolMaxTokens: cfg.ToolMaxTokens, OnUsage: func(u deepseek.Usage) { usage = append(usage, u) }})
	if err != nil {
		t.Fatal("verifier model initialization failed")
	}
	client := deepRepairGuardProvider(provider)
	results := []row{}
	destination := os.Getenv("WECHAT_DEEP_REPAIR_TOOL_VERIFIER_REPORT")
	if destination == "" {
		t.Fatal("verifier report path required")
	}
	completed := map[string]bool{}
	if previous, readErr := os.ReadFile(destination); readErr == nil {
		if os.Getenv("WECHAT_DEEP_REPAIR_TOOL_VERIFIER_RESUME") != "1" {
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
		report := map[string]any{"version": "deep-repair-tool-verifier-v1", "production_sha256": manifest.SHA256, "production_hash_method": manifest.Method, "protocol_sha256": protocolHash, "fixture_sha256": fixtureHash, "config_sha256": configHash, "driver_sha256": manifest.Driver, "model_config": modelConfig, "cases": len(results), "requested_cases": len(samples), "bad_cases": bad, "bad_rejected": rejected, "good_cases": good, "good_accepted": accepted, "passed": passed, "partial": partial, "synthetic_only": true, "editor_calls": 0, "generation_calls": 0, "wechat_sends": 0, "method": "production read-only check of fixed drafts; no regeneration or editor; bad must explicitly reject all, good allow >=95%; not human acceptance", "results": results, "usage": usage, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH}
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
		recording := &deepRepairRecordingModel{client: client, chatModel: cfg.Model, captureGroundingRequests: true}
		engine, in := deepToolVerifierFixture(t, sample)
		deepRepairApplyReviewProfile(&engine.Config, reviewProfile)
		engine.model = recording
		store, c := engine.Store, engine.Config
		proof, ok := store.SourceForInput(in)
		if !ok {
			t.Fatal("tool trigger proof unavailable")
		}
		rc := ReplyContext{TargetIDs: []string{in.Sender}, SourceEventIDs: []string{proof.EventID}}
		draft, normalizeErr := engine.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{sample.Draft}, ReplyContexts: []ReplyContext{rc}})
		if normalizeErr != nil {
			t.Fatal(normalizeErr)
		}
		draft.ExpectedVoiceID = sample.CandidateVoice
		before, _ := json.Marshal(draft)
		usageStart := len(usage)
		callStart := time.Now()
		ctx, cancel := context.WithTimeout(t.Context(), deepRepairOuterDeadline(engine.Config, 1))
		client.Begin(sample.ID, "fixed_review")
		verdict, checkErr := engine.checkReplyGrounding(deepseek.WithPurpose(ctx, "synthetic_fixed_verifier"), in, draft)
		cancel()
		after, _ := json.Marshal(draft)
		r := row{Tool: sample.Tool, ActualResult: sample.Result, ExecutionEvidence: engine.preparedExecutionEvidence(t.Context(), in), ExpectedVoiceID: in.ExpectedVoiceID, ID: sample.ID, Scene: sample.Scene, ExpectedAllow: sample.Allow, OriginalDraft: sample.Draft, Input: in, Evidence: store.Context(in, c, time.Now()), Binding: draft.ReplyContexts, Check: verdict, Calls: recording.responses, Usage: append([]deepseek.Usage(nil), usage[usageStart:]...), Unchanged: string(before) == string(after), ElapsedMS: float64(time.Since(callStart)) / float64(time.Millisecond)}
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
