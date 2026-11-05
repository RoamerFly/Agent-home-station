package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/pluginhost"
)

func TestNamedVoiceIsDiscoverableWithoutBailianKeyAndNeverFallsBack(t *testing.T) {
	f, p, _, _, _ := namedSpeechContractFixture(t)
	{
		legacySpeechFixture := readLegacySpeechTestConfig(t, f.manager)
		legacySpeechFixture.Key = ""
		legacySpeechFixture.TTSEnabled = false
		writeLegacySpeechMigrationFixture(t, f.manager, legacySpeechFixture)
	}
	tools := p.speechTools()
	if len(tools) != 1 || tools[0].Name != "speech_synthesize" || !strings.Contains(string(tools[0].InputSchema), "voice_id") {
		t.Fatal("configured named TTS hidden by unrelated cloud key", tools)
	}
	if _, err := p.generateSpeech(t.Context(), "speech_synthesize", json.RawMessage(`{"voice_id":"missing","text":"hello"}`)); err == nil || !strings.Contains(err.Error(), "voice_not_found") {
		t.Fatal("unknown voice fell through to default provider", err)
	}
	p.trace.Sender, p.trace.Thread = "ordinary", "ordinary"
	f.manager.config.PrivateEnabled = true
	f.manager.config.DirectIDs = append(f.manager.config.DirectIDs, "ordinary")
	if _, err := p.synthesizeLibraryVoice(t.Context(), json.RawMessage(`{"voice_id":"synthetic-named-voice","text":"hello"}`)); err == nil || !strings.Contains(err.Error(), "not_authorized") {
		t.Fatal("ordinary member obtained private voice", err)
	}
}

func TestNamedSpeechToolAdvertisesOnlyNamedOptionsAndSpeedFactor(t *testing.T) {
	_, p, _, _, _ := namedSpeechContractFixture(t)
	tools := p.speechTools()
	if len(tools) != 1 || tools[0].Name != "speech_synthesize" {
		t.Fatal("named speech tool missing", tools)
	}
	schema := string(tools[0].InputSchema)
	if strings.Contains(schema, `"instruction"`) || strings.Contains(tools[0].Description, "instruction?") {
		t.Fatalf("named speech exposed cloud-only instruction: %s", schema)
	}
	if !strings.Contains(schema, `"speed_factor"`) || !strings.Contains(tools[0].Description, "parameters.speed_factor") {
		t.Fatalf("named speech did not document its supported speed control: %s / %s", schema, tools[0].Description)
	}
}

func TestNamedSpeechBadArgumentsNeverClaimAmbiguousOperation(t *testing.T) {
	f, p, _, _, _ := namedSpeechContractFixture(t)
	count := func() int {
		f.manager.agent.mu.Lock()
		defer f.manager.agent.mu.Unlock()
		return len(f.manager.agent.data.Operations)
	}
	before := count()
	_, err := p.Call(t.Context(), "speech_synthesize", json.RawMessage(`{"voice_id":"synthetic-named-voice","text":"慢一点","instruction":"slow"}`))
	if err == nil || !strings.Contains(err.Error(), "named_speech_unsupported_argument: instruction") {
		t.Fatalf("named argument failure was not returned to the caller: %v", err)
	}
	if got := count(); got != before {
		t.Fatalf("invalid named request claimed a durable operation: before=%d after=%d", before, got)
	}
}

func TestJointSpeechToolKeepsDefaultInstructionSeparateFromNamedTuning(t *testing.T) {
	_, p, policy, _, _ := namedSpeechContractFixture(t)
	installed, err := policy.store.Installed(t.Context(), "speech")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = policy.store.SetEnabled(t.Context(), "speech", installed.Revision, false); err != nil {
		t.Fatal(err)
	}
	current, err := policy.store.Configuration(t.Context(), "speech")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = policy.store.SaveConfiguration(t.Context(), "speech", pluginhost.ConfigUpdate{Revision: current.Revision, Values: map[string]json.RawMessage{
		"local_profile_id": json.RawMessage(`"gpt-sovits"`),
		"tts_endpoint":     json.RawMessage(`"https://provider.example/v1/tts"`),
		"tts_model":        json.RawMessage(`"synthetic"`),
		"default_voice":    json.RawMessage(`"synthetic-default"`),
	}, Secrets: map[string]string{"tts_key": "synthetic-key"}}); err != nil {
		t.Fatal(err)
	}
	installed, err = policy.store.Installed(t.Context(), "speech")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = policy.store.SetEnabled(t.Context(), "speech", installed.Revision, true); err != nil {
		t.Fatal(err)
	}
	tools := p.speechTools()
	if len(tools) != 1 {
		t.Fatal("joint speech tool missing", tools)
	}
	schema := string(tools[0].InputSchema)
	for _, want := range []string{`"instruction"`, `"speed_factor"`, `"allOf"`, `"voice_id"`} {
		if !strings.Contains(schema, want) {
			t.Fatalf("joint schema lost %s: %s", want, schema)
		}
	}
	if !strings.Contains(tools[0].Description, "Omit voice_id") {
		t.Fatal("joint tool no longer describes the default route", tools[0].Description)
	}
}

func completeSyntheticNamedSpeech(t *testing.T) (*webFixture, *permittedTools, *namedSpeechPluginPolicy) {
	t.Helper()
	f, p, policy, _, cap := namedSpeechContractFixture(t)
	var err error
	// Remember is part of the request authority. Mint a new request after
	// enabling it so durable asset assertions do not alter an existing grant.
	f.manager.config.Companion.Permissions.Remember = true
	p.trace.TraceID += "-remember"
	raw, _ := json.Marshal(policy.request)
	policy, err = p.newNamedSpeechPluginPolicy(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	completeNamedSpeechCheckpoint(t, policy, cap)
	return f, p, policy
}

func completeNamedSpeechCheckpoint(t *testing.T, policy *namedSpeechPluginPolicy, cap pluginhost.Capability) {
	t.Helper()
	job, _, err := policy.store.CreateJob(t.Context(), pluginhost.Job{Plugin: "speech", Tool: "speech.synthesize_named", Version: policy.packageVersion, PackageDigest: policy.packageDigest, ConfigRevision: strconv.Itoa(policy.configRevision), Context: policy.invocation, Arguments: policy.arguments})
	if err != nil {
		t.Fatal(err)
	}
	job, err = policy.store.ClaimJob(t.Context(), job.ID, job.Revision)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	policy.run = namedSpeechSyntheticRunner(t, policy, &calls)
	reply, err := policy.Service(t.Context(), job, cap, "wechat/local_speech", policy.arguments)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := policy.Validate(t.Context(), job, cap, reply)
	if err != nil {
		t.Fatal(err)
	}
	policy.completedJob, err = policy.store.CompleteJobWithPrivate(t.Context(), job.ID, job.Revision, nil, verified.Result, verified.Private)
	if err != nil || calls != 1 {
		t.Fatal("synthetic inference did not produce one completed checkpoint", err, calls)
	}
}

func TestNamedVoiceCompletedCheckpointStagesActualWAVAndDurableVoiceIdentity(t *testing.T) {
	f, p, policy := completeSyntheticNamedSpeech(t)
	native := &capturedVoiceNative{fakeTools: &fakeTools{fakeNative: f.native}}
	f.manager.native = native
	result, err := policy.stage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if json.Unmarshal([]byte(result), &value) != nil || value["voice_id"] != policy.resource.Voice.ID || value["synthesized"] != true || value["wechat_sends"] != float64(0) || len(native.parts) != 1 {
		t.Fatal(result, len(native.parts))
	}
	assets, err := p.manager.loadAssets()
	if err != nil {
		t.Fatal(err)
	}
	namedAsset := false
	for _, a := range assets {
		if a.VoiceID == policy.resource.Voice.ID && a.VoiceEngine == policy.resource.Voice.Engine {
			namedAsset = true
			if !a.SynthesisTextKnown || a.SynthesisText != policy.request.Text {
				t.Fatal("named voice asset lost verified synthesis text", a)
			}
		}
	}
	if !namedAsset {
		t.Fatal("named voice identity missing from durable asset")
	}
}

func TestNamedVoiceRevokedCompletedCheckpointCannotStage(t *testing.T) {
	f, _, policy := completeSyntheticNamedSpeech(t)
	native := &capturedVoiceNative{fakeTools: &fakeTools{fakeNative: f.native}}
	f.manager.native = native
	f.manager.config.Companion.Permissions.SendVoice = false
	if _, err := policy.stage(t.Context()); err == nil || len(native.parts) != 0 {
		t.Fatal("revoked named voice staged media", err, len(native.parts))
	}
}

func TestNamedVoicePrivateCrossConversationRevocationBlocksReadAndStage(t *testing.T) {
	f, _, policy := completeSyntheticNamedSpeech(t)
	native := &capturedVoiceNative{fakeTools: &fakeTools{fakeNative: f.native}}
	f.manager.native = native
	cfg := f.manager.agent.config()
	cfg.CrossConversation = false
	if err := f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := policy.readAudio(t.Context()); err == nil {
		t.Fatal("private named voice remained readable after global access revocation")
	}
	if _, err := policy.stage(t.Context()); err == nil || len(native.parts) != 0 {
		t.Fatal("private named voice staged after global access revocation", err, len(native.parts))
	}
}

func TestNamedVoicePrivateRequiresCrossConversationBeforeResourceUse(t *testing.T) {
	f, p, voice, _ := voiceAgentFixture(t)
	cfg := f.manager.agent.config()
	cfg.Host.Mode = "full"
	cfg.CrossConversation = false
	if err := f.manager.agent.save(cfg); err != nil {
		t.Fatal(err)
	}
	f.manager.config.Companion.Permissions.SendVoice = true
	request, _ := json.Marshal(map[string]any{"voice_id": voice.ID, "text": "synthetic denied request"})
	if _, err := p.newNamedSpeechPluginPolicy(t.Context(), request); err == nil || !strings.Contains(err.Error(), "owner_account_global_access") {
		t.Fatal("private voice bypassed existing global-access requirement", err)
	}
}

func TestNamedVoiceLocalTextPreservesLongExplicitRequest(t *testing.T) {
	_, access, original, _, _ := namedSpeechContractFixture(t)
	request := original.request
	request.Text = strings.Repeat("长文。", 1000)
	request.Timeout = 31.125
	raw, _ := json.Marshal(request)
	policy, err := access.newNamedSpeechPluginPolicy(t.Context(), raw)
	if err != nil {
		t.Fatal("local text inherited an unrelated provider limit", err)
	}
	var args struct {
		Text    string  `json:"text"`
		Timeout float64 `json:"timeout_seconds"`
	}
	if json.Unmarshal(policy.arguments, &args) != nil || args.Text != request.Text || args.Timeout != 31.125 {
		t.Fatal("explicit local text or fractional timeout was changed")
	}
}

func TestNamedVoiceDoesNotExecuteLegacyEntryWithoutInstalledPlugin(t *testing.T) {
	f, p, policy, _, _ := namedSpeechContractFixture(t)
	manifestPath := filepath.Join(p.executionContext().Public, "manifests", "gpt-sovits.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]string
	if json.Unmarshal(raw, &manifest) != nil {
		t.Fatal("fixture manifest invalid")
	}
	manifest["entry"] = filepath.Join(manifest["root"], "must-never-execute.py")
	encoded, _ := json.Marshal(manifest)
	if err = os.WriteFile(manifestPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	installed, err := policy.store.Installed(t.Context(), "speech")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = policy.store.SetEnabled(t.Context(), "speech", installed.Revision, false); err != nil {
		t.Fatal(err)
	}
	if _, err = p.synthesizeLibraryVoice(t.Context(), json.RawMessage(`{"voice_id":"synthetic-named-voice","text":"hello"}`)); err == nil || !strings.Contains(err.Error(), "plugin_unavailable") {
		t.Fatal("disabled plugin allowed legacy source execution", err)
	}
	if f.native.loginCalls.Load() != 0 {
		t.Fatal("missing plugin touched native WeChat")
	}
}

// This probe executes only synthetic files in the real OS sandbox. It proves
// file-level grants work without granting the private library directory.
func TestLiveNamedSpeechExactSourceFileMounts(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in named speech source-file sandbox probe")
	}
	python, err := filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	_, _, policy, job, cap := namedSpeechContractFixture(t)
	base := policy.p.manager.privateVoiceLibrary()
	sentinel := filepath.Join(base, "files", "unselected-private-sentinel.txt")
	if err = os.WriteFile(sentinel, []byte("synthetic private file outside selected resources"), 0600); err != nil {
		t.Fatal(err)
	}
	helper, err := filepath.Abs("../../scripts/windows_agent_host.pyw")
	if err != nil {
		t.Fatal(err)
	}
	spool := t.TempDir()
	calls := 0
	synthetic := namedSpeechSyntheticRunner(t, policy, &calls)
	policy.run = func(ctx context.Context, cfg agenthost.Config, cwd string, argv []string) ([]byte, []byte, error) {
		selected := []string{namedSpeechArg(argv, "--gpt"), namedSpeechArg(argv, "--sovits"), namedSpeechArg(argv, "--reference"), namedSpeechArg(argv, "--prompt-text-file")}
		for _, path := range cfg.Isolation.ReadOnly {
			if path == base || path == filepath.Join(base, "files") {
				t.Fatal("inference obtained whole private voice library grant")
			}
		}
		for _, selectedPath := range selected {
			found := false
			for _, granted := range cfg.Isolation.ReadOnly {
				found = found || granted == selectedPath
			}
			if !found {
				t.Fatal("selected file lacks exact read-only grant", selectedPath)
			}
		}
		cfg.Isolation.ReadOnly = append(cfg.Isolation.ReadOnly, filepath.Dir(python))
		if runtime.GOOS == "windows" {
			cfg.Windows = &agenthost.WindowsConfig{Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), Helper: helper, Spool: spool}
		}
		probe := `import pathlib,sys
for name in sys.argv[1:5]:
 p=pathlib.Path(name)
 assert p.read_bytes()
 try: p.write_bytes(b'forbidden')
 except PermissionError: pass
 else: raise AssertionError('selected file was writable')
try: pathlib.Path(sys.argv[5]).read_bytes()
except PermissionError: pass
else: raise AssertionError('unselected private file readable')
print('exact_files_verified')
`
		args := append([]string{python, "-X", "utf8", "-c", probe}, selected...)
		args = append(args, sentinel)
		out, err := agenthost.RunIsolated(ctx, cfg, cwd, args)
		if err != nil || strings.TrimSpace(string(out)) != "exact_files_verified" {
			var diagnostic []byte
			if runtime.GOOS == "windows" {
				logs, _ := filepath.Glob(filepath.Join(spool, "broker-*", "stderr.log"))
				for _, log := range logs {
					data, _ := os.ReadFile(log)
					diagnostic = append(diagnostic, data...)
				}
			}
			t.Fatalf("exact selected-file sandbox probe failed: %v %s %s", err, out, diagnostic)
		}
		return synthetic(ctx, cfg, cwd, argv)
	}
	if _, err = policy.Service(t.Context(), job, cap, "wechat/local_speech", policy.arguments); err != nil || calls != 1 {
		t.Fatal("exact file broker failed", err, calls)
	}
}
