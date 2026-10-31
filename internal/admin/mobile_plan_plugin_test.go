package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wechat-bot/internal/deepseek"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/mobile"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

type fakeMobilePlanModel struct {
	visionPrompt string
	visionImages []deepseek.Image
	vision       string
	answer       string
	answers      []string
	visionErr    error
	chatErr      error
	chatCalls    int
}

func TestMobilePlanCanonicalPreservesUnicodeSummaryLimit(t *testing.T) {
	step := mobile.Step{Action: "wait", Summary: strings.Repeat("📝", 800)}
	raw, _ := json.Marshal(map[string]any{"action": "tool", "tool": "device_step", "arguments": step})
	parsed, ok := canonicalMobilePlanStep(string(raw))
	if !ok || !mobile.ValidStep(parsed, mobile.Observation{}) || parsed.Summary != step.Summary {
		t.Fatal("canonical reply imposed a byte ceiling below the Unicode protocol limit")
	}
}

type mobilePlanDeviceStub struct {
	settings mobile.Settings
	task     mobile.Task
}

func (s *mobilePlanDeviceStub) Settings() mobile.Settings         { return s.settings }
func (s *mobilePlanDeviceStub) Save(v mobile.Settings) error      { s.settings = v; return nil }
func (s *mobilePlanDeviceStub) List(*mobile.Source) []mobile.Task { return []mobile.Task{s.task} }
func (s *mobilePlanDeviceStub) Get(id string) (mobile.Task, bool) {
	return s.task, id == s.task.ID
}
func (*mobilePlanDeviceStub) Start(mobile.Source, string, string) (mobile.Task, error) {
	return mobile.Task{}, errors.New("not_used")
}
func (*mobilePlanDeviceStub) Cancel(string, mobile.Source) error { return errors.New("not_used") }
func (*mobilePlanDeviceStub) Busy() bool                         { return false }
func (*mobilePlanDeviceStub) StopAll() error                     { return nil }
func (*mobilePlanDeviceStub) Close() error                       { return nil }
func (*mobilePlanDeviceStub) Devices(context.Context) (json.RawMessage, error) {
	return nil, errors.New("not_used")
}
func (*mobilePlanDeviceStub) Screenshot(string, string) ([]byte, error) {
	return nil, errors.New("not_used")
}
func (*mobilePlanDeviceStub) Artifact(string, string) (mobile.Artifact, []byte, error) {
	return mobile.Artifact{}, nil, errors.New("not_used")
}
func (*mobilePlanDeviceStub) InitializationError() error { return nil }

func (m *fakeMobilePlanModel) Vision(_ context.Context, prompt string, images []deepseek.Image) (string, error) {
	m.visionPrompt = prompt
	m.visionImages = append([]deepseek.Image(nil), images...)
	return m.vision, m.visionErr
}

func (m *fakeMobilePlanModel) ChatFunctions(_ context.Context, _ string, _ []map[string]string, tools []mcptools.Tool) (string, error) {
	m.chatCalls++
	if len(tools) != 1 || tools[0].Name != "device_step" {
		return "", errors.New("unexpected_tool")
	}
	if m.chatErr != nil {
		return "", m.chatErr
	}
	if len(m.answers) > 0 {
		answer := m.answers[0]
		m.answers = m.answers[1:]
		return answer, nil
	}
	return m.answer, nil
}

func mobilePlanPolicyFixture(t *testing.T) (*mobilePlanPluginPolicy, pluginhost.Job, pluginhost.Capability, *fakeMobilePlanModel) {
	t.Helper()
	task := mobile.Task{ID: "task-a", RequestHash: "request-hash", DeviceID: "phone-a", Goal: "打开已授权网页", Steps: 2}
	obs := mobile.Observation{ID: "obs-a", DeviceID: "phone-a", Package: "com.example.home", Width: 1080, Height: 2400, PNG: []byte("synthetic-png"), AllowedPackages: []string{"com.example.home", "com.browser"}, Browsers: []string{"com.browser"}, WebHosts: []string{"example.test"}, Nodes: []json.RawMessage{json.RawMessage(`{"text":"original"}`)}}
	source := mobilePlanSourceVersion(task, obs, 3, "model-fingerprint")
	args, err := json.Marshal(map[string]any{
		"task_id": task.ID, "observation_id": obs.ID, "source_version": source,
		"task":            map[string]any{"goal": task.Goal, "device_id": task.DeviceID, "recordings": task.Artifacts, "recent_executed_steps": task.Events},
		"observation":     map[string]any{"current_app": obs.Package, "allowed_packages": obs.AllowedPackages, "browsers": obs.Browsers, "web_hosts": obs.WebHosts, "width": obs.Width, "height": obs.Height, "ui_nodes": obs.Nodes},
		"allowed_actions": []string{"open_url"},
	})
	if err != nil {
		t.Fatal(err)
	}
	model := &fakeMobilePlanModel{vision: "普通页面，没有敏感弹窗"}
	p := &mobilePlanPluginPolicy{
		task: task, obs: obs, allowedActions: []string{"open_url"}, sourceVersion: source, arguments: args,
		model: model, invocation: pluginhost.Invocation{Account: "test", Thread: "room", Requester: "owner", Purpose: task.Goal, RequestKey: "mobile-plan-test", SourceVersions: map[string]int{task.ID: task.Steps + 1}, Authorization: source},
		checkOverride: func(context.Context, pluginhost.Job, pluginhost.Capability) error { return nil },
	}
	job := pluginhost.Job{Plugin: mobilePlannerPluginID, Tool: mobilePlannerCapability, Context: p.invocation, Arguments: args}
	return p, job, pluginhost.Capability{ID: mobilePlannerCapability}, model
}

func mobilePlanServiceRequest(p *mobilePlanPluginPolicy, operation string) []byte {
	if operation == "describe_screen" {
		b, _ := json.Marshal(map[string]any{"operation": operation, "observation_id": p.obs.ID, "source_version": p.sourceVersion, "prompt": "客观描述当前屏幕"})
		return b
	}
	input, _ := p.planInput()
	b, _ := json.Marshal(map[string]any{
		"operation": operation, "observation_id": p.obs.ID, "source_version": p.sourceVersion, "system": "依据已授权屏幕观察，选择一个安全动作。",
		"turns": []map[string]string{{"role": "user", "content": input}},
		"tools": []map[string]string{{"name": "device_step", "description": "选择一个允许的动作。", "schema": `{"type":"object"}`}},
	})
	return b
}

func mobilePlanRepairServiceRequest(p *mobilePlanPluginPolicy) []byte {
	input, _ := p.planInput()
	b, _ := json.Marshal(map[string]any{
		"operation": "plan_step", "observation_id": p.obs.ID, "source_version": p.sourceVersion, "system": "依据已授权屏幕观察，选择一个安全动作。",
		"turns": []map[string]string{{"role": "user", "content": input}, {"role": "user", "content": "修复工具调用格式。"}},
		"tools": []map[string]string{{"name": "device_step", "description": "选择一个允许的动作。", "schema": `{"type":"object"}`}},
	})
	return b
}

func TestMobilePlanPluginPolicyBindsScreenshotAndActualModelToolReply(t *testing.T) {
	p, job, cap, model := mobilePlanPolicyFixture(t)
	step := mobile.Step{Action: "open_url", Summary: "打开已授权网页", Arguments: map[string]any{"package": "com.browser", "url": "https://example.test"}}
	stepRaw, _ := json.Marshal(step)
	model.answer = `{"action":"tool","tool":"device_step","arguments":` + string(stepRaw) + `}`
	if _, err := p.Service(context.Background(), job, cap, "wechat/model_request", mobilePlanServiceRequest(p, "describe_screen")); err != nil {
		t.Fatal(err)
	}
	if model.visionPrompt != "客观描述当前屏幕" || len(model.visionImages) != 1 || string(model.visionImages[0].Data) != "synthetic-png" {
		t.Fatalf("current private screenshot was not bound to vision call: %#v", model)
	}
	if _, err := p.Service(context.Background(), job, cap, "wechat/model_request", mobilePlanServiceRequest(p, "plan_step")); err != nil {
		t.Fatal(err)
	}
	result, _ := json.Marshal(map[string]any{"task_id": p.task.ID, "observation_id": p.obs.ID, "source_version": p.sourceVersion, "step": step})
	verified, err := p.Validate(context.Background(), job, cap, result)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Private == nil || model.chatCalls != 1 {
		t.Fatalf("unverified result or unexpected provider calls: %#v calls=%d", verified, model.chatCalls)
	}
}

func TestMobilePlanPluginPolicyRejectsDuplicateAndInventedStep(t *testing.T) {
	p, job, cap, model := mobilePlanPolicyFixture(t)
	if _, err := p.Service(context.Background(), job, cap, "wechat/model_request", []byte(`{"operation":"describe_screen","operation":"describe_screen"}`)); err == nil {
		t.Fatal("duplicate request field accepted")
	}
	step := mobile.Step{Action: "open_url", Arguments: map[string]any{"package": "com.browser", "url": "https://example.test"}}
	stepRaw, _ := json.Marshal(step)
	model.answer = `{"action":"tool","tool":"device_step","arguments":` + string(stepRaw) + `}`
	if _, err := p.Service(context.Background(), job, cap, "wechat/model_request", mobilePlanServiceRequest(p, "describe_screen")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Service(context.Background(), job, cap, "wechat/model_request", mobilePlanServiceRequest(p, "plan_step")); err != nil {
		t.Fatal(err)
	}
	invented := mobile.Step{Action: "wait", Summary: "not from provider"}
	result, _ := json.Marshal(map[string]any{"task_id": p.task.ID, "observation_id": p.obs.ID, "source_version": p.sourceVersion, "step": invented})
	if _, err := p.Validate(context.Background(), job, cap, result); err == nil {
		t.Fatal("plugin-invented action accepted")
	}
}

func TestMobilePlanPluginPolicyDoesNotRetryUnknownOrReplaceValidProviderResult(t *testing.T) {
	p, job, cap, model := mobilePlanPolicyFixture(t)
	if _, err := p.Service(t.Context(), job, cap, "wechat/model_request", mobilePlanServiceRequest(p, "describe_screen")); err != nil {
		t.Fatal(err)
	}
	model.chatErr = errors.New("provider_timeout")
	if _, err := p.Service(t.Context(), job, cap, "wechat/model_request", mobilePlanServiceRequest(p, "plan_step")); err == nil {
		t.Fatal("unknown provider error accepted")
	}
	if _, err := p.Service(t.Context(), job, cap, "wechat/model_request", mobilePlanRepairServiceRequest(p)); err == nil || model.chatCalls != 1 {
		t.Fatalf("unknown provider outcome retried: %v calls=%d", err, model.chatCalls)
	}

	p, job, cap, model = mobilePlanPolicyFixture(t)
	step := mobile.Step{Action: "wait", Summary: "等待"}
	stepRaw, _ := json.Marshal(step)
	model.answer = `{"action":"tool","tool":"device_step","arguments":` + string(stepRaw) + `}`
	if _, err := p.Service(t.Context(), job, cap, "wechat/model_request", mobilePlanServiceRequest(p, "describe_screen")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Service(t.Context(), job, cap, "wechat/model_request", mobilePlanServiceRequest(p, "plan_step")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Service(t.Context(), job, cap, "wechat/model_request", mobilePlanRepairServiceRequest(p)); err == nil || model.chatCalls != 1 {
		t.Fatalf("valid provider result was replaced: %v calls=%d", err, model.chatCalls)
	}
}

func TestMobilePlanPluginPolicyRejectsDuplicateKeysInsidePlanningContentBeforeModel(t *testing.T) {
	for name, replace := range map[string]func(string) string{
		"goal": func(input string) string {
			return strings.Replace(input, `"goal":"打开已授权网页"`, `"goal":"attacker","goal":"打开已授权网页"`, 1)
		},
		"nested UI node": func(input string) string {
			return strings.Replace(input, `{"text":"original"}`, `{"text":"attacker","text":"original"}`, 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			p, job, cap, model := mobilePlanPolicyFixture(t)
			if _, err := p.Service(t.Context(), job, cap, "wechat/model_request", mobilePlanServiceRequest(p, "describe_screen")); err != nil {
				t.Fatal(err)
			}
			input, err := p.planInput()
			if err != nil {
				t.Fatal(err)
			}
			content := replace(input)
			if content == input {
				t.Fatal("test did not introduce a duplicate key")
			}
			raw, _ := json.Marshal(map[string]any{
				"operation": "plan_step", "observation_id": p.obs.ID, "source_version": p.sourceVersion, "system": "plan", "turns": []map[string]string{{"role": "user", "content": content}},
				"tools": []map[string]string{{"name": "device_step", "description": "plan", "schema": `{"type":"object"}`}},
			})
			if _, err := p.Service(t.Context(), job, cap, "wechat/model_request", raw); err == nil || model.chatCalls != 0 {
				t.Fatalf("duplicate embedded planning input reached model: %v calls=%d", err, model.chatCalls)
			}
		})
	}
}

func TestMobilePlanActionAllowedKeepsTerminalChoicesAndRejectsUnapprovedAction(t *testing.T) {
	for _, action := range []string{"finish", "blocked", "wait"} {
		if !mobilePlanActionAllowed(mobile.Step{Action: action}, nil) {
			t.Fatal("terminal planner result rejected", action)
		}
	}
	if mobilePlanActionAllowed(mobile.Step{Action: "tap"}, []string{"open_url"}) {
		t.Fatal("unapproved action accepted")
	}
}

func TestMobilePlannerReadyNeverInstallsOrStartsMissingPlugin(t *testing.T) {
	f := setup(t)
	if err := f.manager.mobilePlannerReady(context.Background()); err == nil || err.Error() != "device_planner_plugin_required" {
		t.Fatalf("missing plugin readiness error = %v", err)
	}
	store, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Installed(context.Background(), mobilePlannerPluginID); err == nil {
		t.Fatal("readiness installed a plugin")
	}
}

func TestNewMobilePlanPluginPolicyNormalizesEmptyListsBeforePluginInput(t *testing.T) {
	f := setup(t)
	store, err := pluginhost.OpenStore(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	prepared, err := store.Prepare(t.Context(), "../../plugins/mobile-control")
	if err != nil {
		t.Fatal(err)
	}
	installed, err := store.Install(t.Context(), prepared.Token, prepared.Digest, prepared.Package.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	if installed, err = store.SetEnabled(t.Context(), installed.Package.ID, installed.Revision, true); err != nil {
		t.Fatal(err)
	}
	f.manager.packages = store
	task := mobile.Task{ID: "task-empty", RequestHash: "request-empty", DeviceID: "phone-empty", Goal: "只观察", State: "running", Source: mobile.Source{Admin: true}, ObservationID: "obs-empty"}
	f.manager.mobile = &mobilePlanDeviceStub{settings: mobile.Settings{Revision: 7, Enabled: true}, task: task}
	policy, err := f.manager.newMobilePlanPluginPolicyWithModel(t.Context(), task, mobile.Observation{ID: "obs-empty", DeviceID: task.DeviceID, Package: "com.example.home", Width: 1, Height: 1, PNG: []byte("synthetic-png")}, &fakeMobilePlanModel{})
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Task struct {
			Recordings []json.RawMessage `json:"recordings"`
			Recent     []json.RawMessage `json:"recent_executed_steps"`
		} `json:"task"`
		Observation struct {
			Packages []json.RawMessage `json:"allowed_packages"`
			Browsers []json.RawMessage `json:"browsers"`
			Hosts    []json.RawMessage `json:"web_hosts"`
			Nodes    []json.RawMessage `json:"ui_nodes"`
		} `json:"observation"`
		Actions []json.RawMessage `json:"allowed_actions"`
	}
	if err := json.Unmarshal(policy.arguments, &root); err != nil {
		t.Fatal(err)
	}
	for _, value := range [][]json.RawMessage{root.Task.Recordings, root.Task.Recent, root.Observation.Packages, root.Observation.Browsers, root.Observation.Hosts, root.Observation.Nodes, root.Actions} {
		if value == nil {
			t.Fatalf("plugin argument serialized an optional list as null: %s", policy.arguments)
		}
	}
	if input, err := policy.planInput(); err != nil || !json.Valid([]byte(input)) {
		t.Fatalf("flattened planner input invalid: %q %v", input, err)
	}
}

// This opt-in test traverses the unmodified official stdio package and the
// real host callback policy.  Its provider is a local fake: it performs no
// network request, device operation, or WeChat action.
func TestLiveMobilePlanPluginContract(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in isolated mobile planner contract")
	}
	python, _ = filepath.Abs(python)
	helper, _ := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	f := setup(t)
	store, err := pluginhost.OpenStore(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	prepared, err := store.Prepare(t.Context(), "../../plugins/mobile-control")
	if err != nil {
		t.Fatal(err)
	}
	installed, err := store.Install(t.Context(), prepared.Token, prepared.Digest, prepared.Package.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	if installed, err = store.SetEnabled(t.Context(), installed.Package.ID, installed.Revision, true); err != nil {
		t.Fatal(err)
	}
	f.manager.packages = store
	task := mobile.Task{ID: "task-live", RequestHash: "request-live", DeviceID: "phone-live", Goal: "打开网页", State: "running", Source: mobile.Source{Admin: true}, ObservationID: "obs-live"}
	stub := &mobilePlanDeviceStub{settings: mobile.Settings{Revision: 3, Enabled: true, Actions: []string{"open_url"}}, task: task}
	f.manager.mobile = stub
	step := mobile.Step{Action: "open_url", Summary: "已规划打开网页", Arguments: map[string]any{"package": "com.browser", "url": "https://example.test"}}
	stepRaw, _ := json.Marshal(step)
	model := &fakeMobilePlanModel{vision: "普通页面", answer: `{"action":"tool","tool":"device_step","arguments":` + string(stepRaw) + `}`}
	obs := mobile.Observation{ID: task.ObservationID, DeviceID: task.DeviceID, Package: "com.example.home", Width: 1080, Height: 2400, PNG: []byte("PNG-SHOULD-NOT-LEAK"), AllowedPackages: []string{"com.browser"}, Browsers: []string{"com.browser"}, WebHosts: []string{"example.test"}}
	policy, err := f.manager.newMobilePlanPluginPolicyWithModel(t.Context(), task, obs, model)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(policy.arguments), "PNG-SHOULD-NOT-LEAK") {
		t.Fatal("raw screenshot leaked to plugin arguments")
	}
	controller, err := pluginruntime.NewController(store, pluginruntime.InstalledExecutor(store, pluginruntime.Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	if err = controller.Start(t.Context(), mobilePlannerPluginID, installed.Revision); err != nil {
		t.Fatal(err)
	}
	request := pluginruntime.CallRequest{Plugin: mobilePlannerPluginID, Capability: mobilePlannerCapability, Context: policy.invocation, Arguments: policy.arguments}
	call, err := controller.Invoke(t.Context(), request, policy)
	if err != nil || call.Job.State != "completed" || model.chatCalls != 1 || len(model.visionImages) != 1 {
		t.Fatalf("stdio plugin contract failed: job=%#v err=%v vision=%d plan=%d", call.Job, err, len(model.visionImages), model.chatCalls)
	}
	if _, err = controller.Invoke(t.Context(), request, policy); err != nil || model.chatCalls != 1 || len(model.visionImages) != 1 {
		t.Fatalf("completed request replayed provider: %v vision=%d plan=%d", err, len(model.visionImages), model.chatCalls)
	}
	original := stub.task
	stub.task.Source.Admin = false
	if _, err = controller.Invoke(t.Context(), request, policy); err == nil {
		t.Fatal("revoked task source accepted")
	}
	stub.task = original
	stub.task.Goal = "changed"
	if _, err = controller.Invoke(t.Context(), request, policy); err == nil {
		t.Fatal("changed task source accepted")
	}
	stub.task = original
	f.manager.mu.Lock()
	f.manager.config.Model = "different-model"
	f.manager.mu.Unlock()
	if _, err = controller.Invoke(t.Context(), request, policy); err == nil {
		t.Fatal("changed model configuration accepted")
	}
}
