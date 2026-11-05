package companion

// Evaluation control only: fatal provider failures leave the unattempted
// catalogue pending. They do not manufacture semantic failures or redraw a
// completed generation. No production or synthetic gold is changed here.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/mcptools"
)

type deepRepairProviderClient interface {
	Chat(context.Context, string, []map[string]string) (string, error)
	ChatFunctions(context.Context, string, []map[string]string, []mcptools.Tool) (string, error)
	ChatGrounding(context.Context, string, []map[string]string, deepseek.GroundingOptions) (string, error)
}

type deepRepairProviderBlock struct {
	Code       string `json:"code"`
	HTTPStatus int    `json:"http_status"`
	Stage      string `json:"stage"`
	Attempts   int    `json:"attempts"`
	CaseID     string `json:"case_id"`
	Phase      string `json:"phase"`
	Interface  string `json:"interface"`
}

type deepRepairProviderGuard struct {
	mu      sync.Mutex
	client  deepRepairProviderClient
	caseID  string
	phase   string
	blocked *deepRepairProviderBlock
	failure error
	// Logical interface calls, not HTTP attempts (the SDK may retry formats).
	calls int
}

func deepRepairGuardProvider(client deepRepairProviderClient) *deepRepairProviderGuard {
	// Only this test binary's explicitly selected offline child fixture can
	// replace the provider. The fixture always fails; it cannot forge a pass.
	if os.Getenv("WECHAT_DEEP_REPAIR_FATAL_FIXTURE_CHILD") == "1" && os.Getenv("WECHAT_DEEP_REPAIR_FATAL_FIXTURE_COUNTER") != "" {
		client = &deepRepairFatalFixture{CounterFile: os.Getenv("WECHAT_DEEP_REPAIR_FATAL_FIXTURE_COUNTER"), Mode: os.Getenv("WECHAT_DEEP_REPAIR_FATAL_FIXTURE_PHASE"), Err: &deepseek.RequestError{Diagnostics: deepseek.Diagnostics{Code: "model_insufficient_balance", HTTPStatus: 402, Stage: "http_status", Attempts: 1}}}
	}
	return &deepRepairProviderGuard{client: client}
}

func (g *deepRepairProviderGuard) Begin(caseID, phase string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.blocked == nil {
		g.caseID, g.phase = caseID, phase
	}
}

func deepRepairFatalProvider(err error) (deepseek.Diagnostics, bool) {
	d, ok := deepseek.ErrorDiagnostics(err)
	return d, ok && ((d.HTTPStatus == 402 && d.Code == "model_insufficient_balance") ||
		((d.HTTPStatus == 401 || d.HTTPStatus == 403) && d.Code == "model_authentication_failed"))
}

func (g *deepRepairProviderGuard) call(method string, invoke func() (string, error)) (string, error) {
	// Serialize a single runner's calls. An already observed fatal error must
	// not be followed by an editor, rubric or another catalogue request.
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failure != nil {
		return "", g.failure
	}
	g.calls++
	out, err := invoke()
	if d, fatal := deepRepairFatalProvider(err); fatal {
		phase := g.phase
		if phase == "generation" && method == "ChatGrounding" {
			phase = "grounding"
		}
		g.blocked = &deepRepairProviderBlock{d.Code, d.HTTPStatus, d.Stage, d.Attempts, g.caseID, phase, method}
		g.failure = err
	}
	return out, err
}

func (g *deepRepairProviderGuard) Chat(ctx context.Context, system string, turns []map[string]string) (string, error) {
	return g.call("Chat", func() (string, error) { return g.client.Chat(ctx, system, turns) })
}
func (g *deepRepairProviderGuard) ChatFunctions(ctx context.Context, system string, turns []map[string]string, tools []mcptools.Tool) (string, error) {
	return g.call("ChatFunctions", func() (string, error) { return g.client.ChatFunctions(ctx, system, turns, tools) })
}
func (g *deepRepairProviderGuard) ChatGrounding(ctx context.Context, system string, turns []map[string]string, options deepseek.GroundingOptions) (string, error) {
	return g.call("ChatGrounding", func() (string, error) { return g.client.ChatGrounding(ctx, system, turns, options) })
}

func (g *deepRepairProviderGuard) Block() *deepRepairProviderBlock {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.blocked == nil {
		return nil
	}
	copy := *g.blocked
	return &copy
}

func (g *deepRepairProviderGuard) FailureText() string {
	if b := g.Block(); b != nil {
		return fmt.Sprintf("%s stage=%s http=%d attempts=%d", b.Code, b.Stage, b.HTTPStatus, b.Attempts)
	}
	return ""
}

func (g *deepRepairProviderGuard) Progress(report map[string]any, planned, attempted []string) {
	done := map[string]bool{}
	for _, id := range attempted {
		done[id] = true
	}
	pending := []string{}
	for _, id := range planned {
		if !done[id] {
			pending = append(pending, id)
		}
	}
	report["runner_control_protocol"] = "fatal-provider-failfast-v1"
	report["attempted_cases"] = len(done)
	report["remaining_cases"] = len(pending) // unattempted, excludes the blocked attempt
	report["pending_case_ids"] = pending
	block := g.Block()
	report["provider_blocked"] = block != nil
	report["offline_provider_fixture"] = os.Getenv("WECHAT_DEEP_REPAIR_FATAL_FIXTURE_CHILD") == "1"
	if b := block; b != nil {
		report["partial"] = true
		report["model_assessment_complete"] = false
		report["assessment_complete"] = false
		report["model_quality_gate_passed"] = nil
		report["release_gate_passed"] = false
		report["provider_block"] = b
		next := ""
		if len(pending) != 0 {
			next = pending[0]
		}
		report["continuation"] = map[string]any{"blocked_case_id": b.CaseID, "blocked_phase": b.Phase, "first_pending_case_id": next, "pending_case_ids": pending, "automatic_resume": false, "regenerate_completed_generation": false, "instruction": "Preserve the blocked attempt and any existing generated candidate; explicit phase-aware continuation is required after provider recovery."}
	}
}

func deepRepairRefuseBlockedResume(raw []byte) error {
	var header struct {
		Blocked bool `json:"provider_blocked"`
	}
	if json.Unmarshal(raw, &header) != nil {
		return errors.New("invalid provider checkpoint header")
	}
	if header.Blocked {
		return errors.New("provider-blocked checkpoint requires explicit phase continuation; do not regenerate completed generation or silently skip the blocked case")
	}
	return nil
}

type deepRepairFatalFixture struct {
	deepStructuredFake
	Calls       int
	Err         error
	CounterFile string
	Mode        string
}

func (f *deepRepairFatalFixture) record() error {
	f.Calls++
	if f.CounterFile != "" {
		file, err := os.OpenFile(f.CounterFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		_, err = file.WriteString("provider-call\n")
		_ = file.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
func (f *deepRepairFatalFixture) Chat(context.Context, string, []map[string]string) (string, error) {
	if err := f.record(); err != nil {
		return "", err
	}
	return "", f.Err
}
func (f *deepRepairFatalFixture) ChatFunctions(ctx context.Context, s string, turns []map[string]string, _ []mcptools.Tool) (string, error) {
	if f.Mode == "rubric" {
		if err := f.record(); err != nil {
			return "", err
		}
		return `{"action":"reply","messages":["晚安。"],"reason":"reply"}`, nil
	}
	return f.Chat(ctx, s, turns)
}
func (f *deepRepairFatalFixture) ChatGrounding(ctx context.Context, s string, turns []map[string]string, _ deepseek.GroundingOptions) (string, error) {
	if f.Mode == "rubric" {
		if err := f.record(); err != nil {
			return "", err
		}
		return syntheticGroundingVerdict(turns, "allow"), nil
	}
	return f.Chat(ctx, s, turns)
}

func TestDeepRepairProviderFatalGuardBlocksEveryInterfaceAndKeepsPending(t *testing.T) {
	f := &deepRepairFatalFixture{Err: &deepseek.RequestError{Diagnostics: deepseek.Diagnostics{Code: "model_insufficient_balance", HTTPStatus: 402, Stage: "http_status", Attempts: 1}}}
	g := deepRepairGuardProvider(f)
	g.Begin("one", "generation")
	_, _ = g.Chat(t.Context(), "", nil)
	g.Begin("two", "rubric")
	_, _ = g.ChatFunctions(t.Context(), "", nil, nil)
	_, _ = g.ChatGrounding(t.Context(), "", nil, deepseek.GroundingOptions{})
	if f.Calls != 1 || g.calls != 1 || g.Block().CaseID != "one" {
		t.Fatal("fatal provider request was repeated", f.Calls, g.Block())
	}
	report := map[string]any{"cases": 1, "requested_cases": 3}
	g.Progress(report, []string{"one", "two", "three"}, []string{"one"})
	if report["attempted_cases"] != 1 || report["remaining_cases"] != 2 || report["partial"] != true || report["model_assessment_complete"] != false {
		t.Fatal("blocked fixture manufactured remaining failures", report)
	}
	raw, _ := json.Marshal(report)
	if deepRepairRefuseBlockedResume(raw) == nil {
		t.Fatal("blocked generation was silently resumable")
	}
}

func TestDeepRepairProviderFatalClassificationRequiresTypedStatusAndCode(t *testing.T) {
	for _, tc := range []struct {
		code   string
		status int
		fatal  bool
	}{{"model_insufficient_balance", 402, true}, {"model_authentication_failed", 401, true}, {"model_authentication_failed", 403, true}, {"model_content_filtered", 403, false}, {"model_http_rejected", 402, false}, {"model_service_unavailable", 503, false}, {"model_rate_limited", 429, false}} {
		_, fatal := deepRepairFatalProvider(&deepseek.RequestError{Diagnostics: deepseek.Diagnostics{Code: tc.code, HTTPStatus: tc.status}})
		if fatal != tc.fatal {
			t.Fatal(tc)
		}
	}
	if _, fatal := deepRepairFatalProvider(errors.New("model_insufficient_balance HTTP402")); fatal {
		t.Fatal("arbitrary text was mistaken for a typed provider failure")
	}
}

// This runs the actual opt-in entry points against a typed HTTP402 fake in a
// child test process. A separate fake-provider call counter proves that each
// real loop stops after one call. There is no network or real API key.
func TestDeepRepairLiveRunnersFailFastOnLocalFatalProvider(t *testing.T) {
	if os.Getenv("WECHAT_DEEP_REPAIR_FATAL_FIXTURE_CHILD") != "" {
		t.Fatal("fatal fixture child must only select a live runner")
	}
	runners := []struct {
		name, reportEnv string
		requested       int
		phase           string
		calls           int
	}{
		{"TestDeepRepairRealModelEvaluation", "WECHAT_DEEP_REPAIR_MODEL_REPORT", 3, "generation", 1},
		{"TestDeepRepairVerifierRealModel", "WECHAT_DEEP_REPAIR_VERIFIER_REPORT", 21, "generation", 1},
		{"TestDeepRepairToolVerifierRealModel", "WECHAT_DEEP_REPAIR_TOOL_VERIFIER_REPORT", 8, "generation", 1},
		{"TestDeepRepairPluralVerifierRealModel", "WECHAT_DEEP_REPAIR_PLURAL_VERIFIER_REPORT", 2, "generation", 1},
		{"TestDeepRepairSourceRoleVerifierRealModel", "WECHAT_DEEP_REPAIR_SOURCE_ROLE_VERIFIER_REPORT", 6, "generation", 1},
		{"TestDeepRepairV7RegressionRealModel", "WECHAT_DEEP_REPAIR_V7_REGRESSION_REPORT", 7, "generation", 1},
		{"TestDeepRepairGeneratedReviewE2ERealModel", "WECHAT_DEEP_REPAIR_REVIEW_E2E_REPORT", 5, "generation", 1},
		{"TestDeepRepairRealModelEvaluation", "WECHAT_DEEP_REPAIR_MODEL_REPORT", 3, "rubric", 2},
		{"TestDeepRepairGeneratedReviewE2ERealModel", "WECHAT_DEEP_REPAIR_REVIEW_E2E_REPORT", 5, "rubric", 2},
	}
	for _, runner := range runners {
		t.Run(runner.name+"/"+runner.phase, func(t *testing.T) {
			dir := t.TempDir()
			counter := filepath.Join(dir, "provider-calls.txt")
			key := filepath.Join(dir, "synthetic-key.txt")
			if err := os.WriteFile(key, []byte("synthetic-offline-key"), 0600); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(dir, "config.json")
			body, _ := json.Marshal(map[string]any{"base_url": "https://offline-evaluation.invalid", "model": "deepseek-flash", "key_file": key, "compatible": true, "max_tokens": 768, "tool_max_tokens": 4096})
			if err := os.WriteFile(config, body, 0600); err != nil {
				t.Fatal(err)
			}
			manifest := filepath.Join(dir, "manifest.json")
			body, _ = json.Marshal(map[string]any{"sha256": strings.Repeat("a", 64), "method": "offline-fatal-fixture", "driver_sha256": strings.Repeat("b", 64)})
			if err := os.WriteFile(manifest, body, 0600); err != nil {
				t.Fatal(err)
			}
			report := filepath.Join(dir, "report.json")
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^"+runner.name+"$", "-test.count=1")
			for _, env := range os.Environ() {
				if !strings.HasPrefix(env, "WECHAT_DEEP_REPAIR_") {
					cmd.Env = append(cmd.Env, env)
				}
			}
			cmd.Env = append(cmd.Env, "WECHAT_DEEP_REPAIR_FATAL_FIXTURE_CHILD=1", "WECHAT_DEEP_REPAIR_FATAL_FIXTURE_COUNTER="+counter, "WECHAT_DEEP_REPAIR_FATAL_FIXTURE_PHASE="+runner.phase, "WECHAT_DEEP_REPAIR_MODEL_CONFIG="+config, "WECHAT_DEEP_REPAIR_SOURCE_MANIFEST="+manifest, runner.reportEnv+"="+report)
			if runner.name == "TestDeepRepairRealModelEvaluation" {
				cmd.Env = append(cmd.Env, "WECHAT_DEEP_REPAIR_MODEL_LIMIT=3", "WECHAT_DEEP_REPAIR_MODEL_GROUP=media")
			}
			output, childErr := cmd.CombinedOutput()
			if childErr == nil || ctx.Err() != nil {
				t.Fatalf("expected a saved fail-fast failure, not success/timeout: %v %s", childErr, output)
			}
			body, err = os.ReadFile(report)
			if err != nil {
				t.Fatalf("missing blocked checkpoint: %v %s", err, output)
			}
			var saved struct {
				Cases, Requested, Attempted, Remaining int
				Partial, ProviderBlocked               bool
				Results                                []json.RawMessage
			}
			var raw map[string]json.RawMessage
			if json.Unmarshal(body, &raw) != nil {
				t.Fatal("invalid checkpoint")
			}
			_ = json.Unmarshal(raw["cases"], &saved.Cases)
			_ = json.Unmarshal(raw["requested_cases"], &saved.Requested)
			_ = json.Unmarshal(raw["attempted_cases"], &saved.Attempted)
			_ = json.Unmarshal(raw["remaining_cases"], &saved.Remaining)
			_ = json.Unmarshal(raw["partial"], &saved.Partial)
			_ = json.Unmarshal(raw["provider_blocked"], &saved.ProviderBlocked)
			_ = json.Unmarshal(raw["results"], &saved.Results)
			count, readErr := os.ReadFile(counter)
			if readErr != nil || string(count) != strings.Repeat("provider-call\n", runner.calls) || saved.Cases != 1 || saved.Requested != runner.requested || saved.Attempted != 1 || saved.Remaining != runner.requested-1 || !saved.Partial || !saved.ProviderBlocked || len(saved.Results) != 1 {
				t.Fatalf("actual runner did not preserve remaining cases: fake-calls=%q checkpoint=%s child=%s", count, body, output)
			}
			if string(raw["model_quality_gate_passed"]) != "null" || string(raw["release_gate_passed"]) != "false" {
				t.Fatal("provider failure was graded as model quality", string(body))
			}
			if runner.phase == "rubric" {
				var result map[string]json.RawMessage
				_ = json.Unmarshal(saved.Results[0], &result)
				messages := result["messages"]
				if decision, ok := result["final_decision"]; ok {
					var final map[string]json.RawMessage
					_ = json.Unmarshal(decision, &final)
					messages = final["messages"]
				}
				var words []string
				_ = json.Unmarshal(messages, &words)
				var continuation map[string]any
				_ = json.Unmarshal(raw["continuation"], &continuation)
				if len(words) == 0 || continuation["blocked_phase"] != "rubric" {
					t.Fatal("completed generation was lost or mislabelled", string(body))
				}
			}
		})
	}
}
