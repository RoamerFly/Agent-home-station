package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

func speechPluginFixture(t *testing.T) (*webFixture, *permittedTools, *speechPluginPolicy) {
	t.Helper()
	f := setup(t)
	s, err := pluginhost.OpenStore(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	// Install the signed official package exactly as shipped. This fixture must
	// not promote pending capabilities or rewrite the package permission set.
	prepared, err := s.Prepare(t.Context(), "../../plugins/speech")
	if err != nil {
		t.Fatal(err)
	}
	pkg := prepared.Package
	i, err := s.Install(t.Context(), prepared.Token, prepared.Digest, pkg.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveConfiguration(t.Context(), pkg.ID, pluginhost.ConfigUpdate{Values: map[string]json.RawMessage{"tts_endpoint": json.RawMessage(`"https://provider.example/api/v1/services/aigc/multimodal-generation/generation"`), "tts_model": json.RawMessage(`"synthetic-tts"`), "default_voice": json.RawMessage(`"synthetic-voice"`), "default_instruction": json.RawMessage(`"friendly"`)}, Secrets: map[string]string{"tts_key": "synthetic-private-key"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetEnabled(t.Context(), pkg.ID, i.Revision, true); err != nil {
		t.Fatal(err)
	}
	f.manager.packages = s
	f.manager.config.Enabled = true
	f.manager.config.Companion.Permissions.SendVoice = true
	p := &permittedTools{manager: f.manager, trace: companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "synthetic-user", TraceID: "synthetic-speech", Text: "请说一句话"}}
	policy, err := p.newSpeechPluginPolicy(t.Context(), json.RawMessage(`{"text":"你好","instruction":"轻一点","timeout_seconds":10}`))
	if err != nil {
		t.Fatal(err)
	}
	return f, p, policy
}

func TestCloudSpeechConfigurationDoesNotEnableNamedVoice(t *testing.T) {
	f, access, policy := speechPluginFixture(t)
	job, capability := speechPluginJob(policy)
	if err := policy.Check(t.Context(), job, capability); err != nil {
		t.Fatalf("cloud-only configuration incorrectly required local voice configuration: %v", err)
	}
	if f.manager.namedSpeechPluginConfigured(t.Context()) {
		t.Fatal("cloud-only installation exposed local voice capability")
	}
	tools := access.speechTools()
	if len(tools) != 1 || tools[0].Name != "speech_synthesize" || strings.Contains(string(tools[0].InputSchema), "voice_id") {
		t.Fatalf("cloud-only tool exposed an unavailable named voice route: %#v", tools)
	}
	if !strings.Contains(string(tools[0].InputSchema), `"instruction"`) {
		t.Fatalf("default cloud speech lost its supported instruction field: %s", tools[0].InputSchema)
	}
}

func speechPluginJob(p *speechPluginPolicy) (pluginhost.Job, pluginhost.Capability) {
	i, _ := p.store.Installed(context.Background(), "speech")
	return pluginhost.Job{Plugin: "speech", Version: i.Package.Version, PackageDigest: i.Digest, ConfigRevision: "1", Tool: "speech.synthesize", Context: p.invocation}, pluginhost.Capability{ID: "speech.synthesize", Permissions: []string{"model.invoke"}}
}

func speechPluginRequest(p *speechPluginPolicy) json.RawMessage {
	instruction, _ := p.expectedInstruction()
	b, _ := json.Marshal(map[string]any{"route": "synthesize", "body": map[string]any{"model": p.model, "input": map[string]any{"text": p.text, "voice": p.voice, "format": "wav", "sample_rate": 24000, "instruction": instruction}}, "max_bytes": 4096, "timeout_seconds": p.timeout})
	return b
}

func TestSpeechPluginBindsDefaultVoiceAndPrivateProviderURL(t *testing.T) {
	f, access, p := speechPluginFixture(t)
	j, cap := speechPluginJob(p)
	calls := 0
	p.post = func(_ context.Context, endpoint string, headers map[string]string, body json.RawMessage, _ int64, _ time.Duration) (json.RawMessage, error) {
		calls++
		if endpoint != "https://provider.example/api/v1/services/aigc/multimodal-generation/generation" || headers["Authorization"] != "Bearer synthetic-private-key" || !bytes.Contains(body, []byte(`"voice":"synthetic-voice"`)) {
			t.Fatal("unbound speech transport")
		}
		return json.RawMessage(`{"model":"synthetic-tts","output":{"audio":{"url":"https://audio.example/private.wav?token=synthetic"}}}`), nil
	}
	request := speechPluginRequest(p)
	bad := bytes.Replace(request, []byte("synthetic-voice"), []byte("other-voice"), 1)
	if _, err := p.Service(t.Context(), j, cap, "wechat/model_request", bad); err == nil || calls != 0 {
		t.Fatal("voice override reached transport", err, calls)
	}
	if _, err := p.Service(t.Context(), j, cap, "wechat/model_request", request); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Service(t.Context(), j, cap, "wechat/model_request", request); err == nil || calls != 1 {
		t.Fatal("duplicate TTS provider call", err, calls)
	}
	model := "synthetic-tts"
	result, _ := json.Marshal(map[string]any{"ok": true, "status": "provider_accepted", "provider_audio_url": "https://audio.example/private.wav?token=synthetic", "provider_model": model, "voice": p.voice, "coverage": "provider_tts_response", "untrusted_external_content": true})
	verified, err := p.Validate(t.Context(), j, cap, result)
	if err != nil || bytes.Contains(verified.Result, []byte("token=synthetic")) || !bytes.Contains(verified.Private, []byte("token=synthetic")) || !bytes.Contains(verified.Result, []byte(`"requested_voice":"synthetic-voice"`)) || !bytes.Contains(verified.Result, []byte(`"provider_voice_status":"not_reported"`)) {
		t.Fatal("private provider URL contract", err, string(verified.Result), string(verified.Private))
	}
	access.trace.Thread = "other"
	if err = p.Check(t.Context(), j, cap); err == nil {
		t.Fatal("context switch accepted")
	}
	access.trace.Thread = "filehelper"
	f.manager.config.Companion.Permissions.SendVoice = false
	if err = p.Check(t.Context(), j, cap); err == nil {
		t.Fatal("voice permission revocation accepted")
	}
}

func TestSpeechPluginRejectsProviderClaimMismatch(t *testing.T) {
	_, _, p := speechPluginFixture(t)
	j, cap := speechPluginJob(p)
	p.post = func(context.Context, string, map[string]string, json.RawMessage, int64, time.Duration) (json.RawMessage, error) {
		return json.RawMessage(`{"model":"synthetic-tts","output":{"audio":{"url":"https://audio.example/a.wav"}}}`), nil
	}
	if _, err := p.Service(t.Context(), j, cap, "wechat/model_request", speechPluginRequest(p)); err != nil {
		t.Fatal(err)
	}
	bad := json.RawMessage(`{"ok":true,"status":"provider_accepted","provider_audio_url":"https://audio.example/other.wav","voice":"synthetic-voice","coverage":"provider_tts_response","untrusted_external_content":true}`)
	if _, err := p.Validate(t.Context(), j, cap, bad); err == nil {
		t.Fatal("forged result URL accepted")
	}
}

func TestSpeechPluginRejectsProviderVoiceMismatch(t *testing.T) {
	_, _, p := speechPluginFixture(t)
	j, cap := speechPluginJob(p)
	p.post = func(context.Context, string, map[string]string, json.RawMessage, int64, time.Duration) (json.RawMessage, error) {
		return json.RawMessage(`{"model":"synthetic-tts","output":{"audio":{"url":"https://audio.example/a.wav","voice":"other-voice"}}}`), nil
	}
	if _, err := p.Service(t.Context(), j, cap, "wechat/model_request", speechPluginRequest(p)); err != nil {
		t.Fatal(err)
	}
	claimed := "synthetic-tts"
	output, _ := json.Marshal(map[string]any{"ok": true, "status": "provider_accepted", "provider_audio_url": "https://audio.example/a.wav", "provider_model": claimed, "voice": "synthetic-voice", "coverage": "provider_tts_response", "untrusted_external_content": true})
	if _, err := p.Validate(t.Context(), j, cap, output); err == nil {
		t.Fatal("provider voice mismatch accepted")
	}
}

func TestSpeechPluginUnicodeLimitsAndTransportTimeout(t *testing.T) {
	_, access, _ := speechPluginFixture(t)
	args, _ := json.Marshal(map[string]any{"text": strings.Repeat("你", 2000), "instruction": strings.Repeat("慢", 1500), "timeout_seconds": 7200})
	p, err := access.newSpeechPluginPolicy(t.Context(), args)
	if err != nil || p == nil || !bytes.Contains(p.arguments, []byte(`"timeout_seconds":7200`)) {
		t.Fatal("valid Unicode length or transport timeout rejected", err)
	}
	args, _ = json.Marshal(map[string]any{"text": strings.Repeat("你", 2001)})
	if _, err = access.newSpeechPluginPolicy(t.Context(), args); err == nil {
		t.Fatal("over-limit Unicode text accepted")
	}
	args, _ = json.Marshal(map[string]any{"text": "x", "instruction": strings.Repeat("慢", 1501)})
	if _, err = access.newSpeechPluginPolicy(t.Context(), args); err == nil {
		t.Fatal("over-limit Unicode instruction accepted")
	}
}

func TestSpeechPluginEqualDefaultInstructionIsNotDuplicated(t *testing.T) {
	_, access, _ := speechPluginFixture(t)
	p, err := access.newSpeechPluginPolicy(t.Context(), json.RawMessage(`{"text":"synthetic","instruction":"friendly"}`))
	if err != nil {
		t.Fatal(err)
	}
	instruction, err := p.expectedInstruction()
	if err != nil || instruction != "friendly" {
		t.Fatalf("instruction=%q err=%v", instruction, err)
	}
}

// Opt-in Windows/Linux isolated-runtime contract.  The only provider response
// is an injected JSON callback; no remote synthesis, download, or WeChat send
// occurs.  This proves the actual stdio plugin and host policy agree on the
// model body and result contract before core-side audio staging is attempted.
func TestLiveSpeechPluginSynthesisContract(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in isolated speech plugin contract")
	}
	python, _ = filepath.Abs(python)
	helper, _ := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	f, _, policy := speechPluginFixture(t)
	calls := 0
	policy.post = func(_ context.Context, _ string, _ map[string]string, body json.RawMessage, _ int64, _ time.Duration) (json.RawMessage, error) {
		calls++
		if !bytes.Contains(body, []byte(`"voice":"synthetic-voice"`)) || !bytes.Contains(body, []byte(`"sample_rate":24000`)) {
			t.Fatal("runtime changed bound provider body")
		}
		return json.RawMessage(`{"model":"synthetic-tts","output":{"audio":{"url":"https://audio.example/private.wav?token=synthetic"}}}`), nil
	}
	c, err := pluginruntime.NewController(policy.store, pluginruntime.InstalledExecutor(policy.store, pluginruntime.Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}))
	if err != nil {
		t.Fatal(err)
	}
	f.manager.packageRuntime, f.manager.packageRuntimeReady = c, true
	defer c.Close()
	result, err := policy.synthesize(t.Context())
	if err != nil || calls != 1 || result.URL != "https://audio.example/private.wav?token=synthetic" || result.RequestedVoice != "synthetic-voice" || result.ProviderVoiceStatus != "not_reported" {
		t.Fatal("isolated speech contract", result, err, calls)
	}
	// This is the official package, without promoting any capabilities.
	// The URL stays in the private checkpoint while the
	// public job result is safe to expose to the rest of the host.
	page, err := policy.store.Jobs(t.Context(), "speech", 0, 8)
	if err != nil || len(page.Items) != 1 || bytes.Contains(page.Items[0].Result, []byte("token=synthetic")) {
		t.Fatal("official cloud checkpoint is not private", err, page)
	}
	private, err := policy.store.PrivateJobResult(t.Context(), page.Items[0], func(ctx context.Context, job pluginhost.Job) error {
		return policy.Check(ctx, job, pluginhost.Capability{})
	})
	if err != nil || !bytes.Contains(private, []byte("token=synthetic")) {
		t.Fatal("official cloud private checkpoint missing", err, string(private))
	}
	if _, err = c.Invoke(t.Context(), pluginruntime.CallRequest{Plugin: "speech", Capability: "speech.voices", Context: policy.invocation, Arguments: json.RawMessage(`{}`)}, policy); err == nil || calls != 1 {
		t.Fatal("pending voice capability dispatched", err, calls)
	}
	caps, err := policy.store.Capabilities(t.Context(), "speech")
	if err != nil || len(caps) != 4 {
		t.Fatal("official capability count changed", len(caps), err)
	}
	want := map[string]bool{"speech.synthesize": true, "speech.synthesize_named": true, "speech.voices": false, "speech.train": false}
	for _, cap := range caps {
		ready, exists := want[cap.ID]
		implementation := "migration-pending"
		if ready {
			implementation = "ready"
		}
		if !exists || cap.Discoverable != ready || cap.Implementation != implementation {
			t.Fatal("official capability state changed", cap.ID, cap.Implementation, cap.Discoverable)
		}
		delete(want, cap.ID)
	}
	if len(want) != 0 {
		t.Fatal("official capabilities missing", want)
	}
}
