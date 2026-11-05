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

type deepVerifierPair struct{ ID, Scene, Bad, Good string }

var deepVerifierPairs = []deepVerifierPair{
	{"relief", "call", "刚聊完视频，那这会儿应该松快些了吧？", "通话刚结束了。想接着聊的话，可以说说。"},
	{"recovery", "call", "刚挂掉那会儿确实会愣一下，现在缓过来了吗？", "刚挂掉。要是想继续聊，我在。"},
	{"conversation-topic", "call", "刚才视频聊了不少家常吧？", "刚挂掉视频。你想说说刚才聊了什么吗？"},
	{"photographer", "bicycle", "这是乙去年亲自拍的黄色自行车。", "乙发的那张图里显示一辆黄色自行车；拍摄者没有来源。"},
	{"visual-ocr", "bicycle", "图上文字标注着“去年拍的黄色自行车”。", "图里显示黄色自行车，没有可核实的画面文字。"},
	{"visual-closeup", "umbrella", "画面是雨伞的近景，其他细节很模糊。", "观察结果只提到雨伞，没有更详细的画面信息。"},
	{"failed-blank", "failed", "这张图片看到的是一片空白。", "这张图没有读取成功，不能确定画面或文字。"},
	{"reporter-participant", "retelling", "你刚和你爸视频结束，想聊聊吗？", "收到，你说的是乙与父亲刚结束的那通视频。"},
	{"same-day-yesterday", "same-day", "你昨晚说过不喝咖啡。", "你刚才说过不喝咖啡。"},
}

// Returns a fully observed synthetic input and source proof. The source author,
// original body, media observation and target are deliberately separate.
func deepVerifierScene(t *testing.T, store *Store, config Config, scene string) (Input, ReplyContext) {
	t.Helper()
	now := time.Now().UTC()
	in := Input{Channel: "wechat", Account: "verifier-synthetic", Thread: "verifier-room@chatroom", Sender: "member-alpha", SenderName: "甲", TraceID: "verifier-current", IsGroup: true, Mode: "direct", Directed: true, MustReply: true}
	source := in
	source.TraceID = "verifier-source"
	source.Text = "刚挂掉视频通话。"
	at := now.Add(-10 * time.Second)
	if at.In(location(config.Timezone)).Format("2006-01-02") != now.In(location(config.Timezone)).Format("2006-01-02") {
		at = now
	}
	source.MessageTime = at.Format(time.RFC3339Nano)
	source.Source = deepRepairSource(source, "verifier-dataset", at)
	source.Source.MessageType = 1
	switch scene {
	case "call":
		in.Text = "刚挂掉。"
	case "bicycle", "umbrella", "failed":
		source.Sender, source.SenderName = "member-beta", "乙"
		source.Source.Sender = source.Sender
		source.Source.MessageType = 3
		original := ""
		source.OriginalText = &original
		observation, state := "[视觉观察] 黄色自行车。", "verified"
		if scene == "umbrella" {
			observation = "[视觉观察] 雨伞。"
		}
		if scene == "failed" {
			observation, state = "[图片观察失败：没有可读画面]", "failed"
		}
		source.Text = observation
		source.IncomingMedia = []IncomingMediaRef{{Source: *source.Source, AssetID: "verifier-image", Kind: "image", UploaderID: source.Sender, Observation: observation, ObservationState: state, ObservationVersion: 1}}
		in.Text = "我引用的这张图片是什么情况？"
		in.QuotedSource = source.Source
	case "retelling":
		source.Sender, source.SenderName = "member-beta", "乙"
		source.Source.Sender = source.Sender
		source.Text = "我刚与自己的父亲视频结束。"
		in.Text = "你爸刚视频，刚挂掉。"
		in.Addressees = []string{source.Sender}
	case "same-day":
		source.Text = "我不喝咖啡。"
		in.Text = "我刚才说喝不喝咖啡？"
	case "greeting":
		source.Text = "你好。"
		in.Text = "你好。"
	default:
		t.Fatal("unknown verifier scene", scene)
	}
	if len(source.IncomingMedia) == 0 {
		literalOriginal := source.Text
		source.OriginalText = &literalOriginal
	}
	if err := store.Observe(source, config, now); err != nil {
		t.Fatal(err)
	}
	proof, ok := store.SourceForInput(source)
	if !ok {
		t.Fatal("synthetic verifier source unavailable")
	}
	in.Source = deepRepairSource(in, "verifier-dataset", now)
	in.Source.MessageType = 1
	in.MessageTime = now.Format(time.RFC3339Nano)
	literalOriginal := in.Text
	in.OriginalText = &literalOriginal
	if err := store.Observe(in, config, now); err != nil {
		t.Fatal(err)
	}
	rc := ReplyContext{TargetIDs: []string{in.Sender}, AnchorMessageID: in.TraceID, SourceEventIDs: []string{proof.EventID}}
	if len(source.IncomingMedia) > 0 {
		rc.MediaSourceIDs = []string{"verifier-image"}
	}
	return in, rc
}

func TestDeepRepairVerifierSyntheticPreparation(t *testing.T) {
	for _, pair := range deepVerifierPairs {
		t.Run(pair.ID, func(t *testing.T) {
			store, config, _ := memoryFixture(t)
			config.Enabled = true
			config.Permissions.ReplyText = true
			in, rc := deepVerifierScene(t, store, config, pair.Scene)
			engine, err := New(config, store, extractionFixture(func(context.Context, string, []map[string]string) (string, error) {
				return "", fmt.Errorf("preparation must not call a model")
			}), "synthetic")
			if err != nil {
				t.Fatal(err)
			}
			decision, err := engine.normalizeReplyContexts(t.Context(), in, Decision{Action: "reply", Messages: []string{pair.Good}, ReplyContexts: []ReplyContext{rc}})
			if err != nil {
				t.Fatal(err)
			}
			if err = engine.ValidateReplyContext(t.Context(), in, decision.ReplyContexts[0]); err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(decision)
			if dest := os.Getenv("WECHAT_DEEP_REPAIR_VERIFIER_PREPARE"); dest != "" {
				if err = os.MkdirAll(dest, 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(dest, pair.ID+".json"), b, 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// Calls the production read-only checker on immutable drafts. No editor,
// regeneration, tool execution or sender is installed in this experiment.
func TestDeepRepairVerifierRealModel(t *testing.T) {
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
		t.Fatal("source manifest required; use scripts/deep_repair_verifier_evaluate.py")
	}
	protocol := sha256.New()
	for _, name := range []string{"deep_repair_verifier_live_test.go", "deep_repair_acceptance_test.go", "deep_repair_provider_block_test.go"} {
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
	type sample struct {
		ID, Scene, Draft string
		Allow            bool
	}
	samples := []sample{}
	for _, pair := range deepVerifierPairs {
		samples = append(samples, sample{pair.ID + "-bad", pair.Scene, pair.Bad, false}, sample{pair.ID + "-good", pair.Scene, pair.Good, true})
	}
	for i, draft := range []string{"你好。", "我在，你说。", "你好，有什么想聊的吗？"} {
		samples = append(samples, sample{fmt.Sprintf("ordinary-control-%d", i), "greeting", draft, true})
	}
	fixtureBytes, _ := json.Marshal(samples)
	fixtureHash := fmt.Sprintf("%x", sha256.Sum256(fixtureBytes))
	type row struct {
		ID            string              `json:"id"`
		Scene         string              `json:"scene"`
		ExpectedAllow bool                `json:"expected_allow"`
		OriginalDraft string              `json:"original_draft"`
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
	destination := os.Getenv("WECHAT_DEEP_REPAIR_VERIFIER_REPORT")
	if destination == "" {
		t.Fatal("verifier report path required")
	}
	completed := map[string]bool{}
	if previous, readErr := os.ReadFile(destination); readErr == nil {
		if os.Getenv("WECHAT_DEEP_REPAIR_VERIFIER_RESUME") != "1" {
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
		report := map[string]any{"version": "deep-repair-verifier-v1", "production_sha256": manifest.SHA256, "production_hash_method": manifest.Method, "protocol_sha256": protocolHash, "fixture_sha256": fixtureHash, "config_sha256": configHash, "driver_sha256": manifest.Driver, "model_config": modelConfig, "cases": len(results), "requested_cases": len(samples), "bad_cases": bad, "bad_rejected": rejected, "good_cases": good, "good_accepted": accepted, "passed": passed, "partial": partial, "synthetic_only": true, "editor_calls": 0, "generation_calls": 0, "wechat_sends": 0, "method": "production read-only check of fixed drafts; no regeneration or editor; bad must explicitly reject all, good allow >=95%; not human acceptance", "results": results, "usage": usage, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH}
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
		in, rc := deepVerifierScene(t, store, c, sample.Scene)
		recording := &deepRepairRecordingModel{client: client, chatModel: cfg.Model, captureGroundingRequests: true}
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
		r := row{ID: sample.ID, Scene: sample.Scene, ExpectedAllow: sample.Allow, OriginalDraft: sample.Draft, Input: in, Evidence: store.Context(in, c, time.Now()), Binding: draft.ReplyContexts, Check: verdict, Calls: recording.responses, Usage: append([]deepseek.Usage(nil), usage[usageStart:]...), Unchanged: string(before) == string(after), ElapsedMS: float64(time.Since(callStart)) / float64(time.Millisecond)}
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
