package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"wechat-bot/internal/mediaspec"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

func installDefaultSpeechFixture(t *testing.T, m *Manager, configured, enabled bool) pluginhost.InstalledPackage {
	t.Helper()
	s, err := m.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Prepare(t.Context(), "../../plugins/speech")
	if err != nil {
		t.Fatal(err)
	}
	i, err := s.Install(t.Context(), p.Token, p.Digest, p.Package.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	if configured {
		_, err = s.SaveConfiguration(t.Context(), "speech", pluginhost.ConfigUpdate{Values: map[string]json.RawMessage{
			"tts_endpoint":  json.RawMessage(`"https://plugin-provider.example/api/v1/services/audio/tts/SpeechSynthesizer"`),
			"tts_model":     json.RawMessage(`"synthetic-plugin-model"`),
			"default_voice": json.RawMessage(`"synthetic-plugin-voice"`),
		}, Secrets: map[string]string{"tts_key": "synthetic-plugin-only-key"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if enabled {
		i, err = s.SetEnabled(t.Context(), "speech", i.Revision, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Never inherit a developer's real deployment runtime in offline tests.
	controller, err := pluginruntime.NewController(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.packageRuntime, m.packageRuntimeReady = controller, false
	return i
}

func setLegacyDefaultSpeechFixture(t *testing.T, m *Manager) {
	m.mu.Lock()
	defer m.mu.Unlock()
	{
		legacySpeechFixture := mediaspec.Defaults()
		legacySpeechFixture.TTSEnabled = true
		legacySpeechFixture.Key = "synthetic-legacy-key-never-dispatched"
		legacySpeechFixture.Voice = "synthetic-legacy-voice-never-dispatched"
		writeLegacySpeechMigrationFixture(t, m, legacySpeechFixture)
	}
	m.config.Enabled = true
	m.config.Companion.Permissions.SendVoice = true
}

func breakDefaultSpeechFixture(t *testing.T, m *Manager, mode string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(m.dir, "plugin-host", "plugins.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Model a retained installation whose private configuration or grant was
	// revoked outside this process. Production authorization must re-read it.
	query := `UPDATE configurations SET secrets_json='{}' WHERE plugin='speech'`
	if mode == "unauthorized" {
		query = `UPDATE installed SET permissions='[]' WHERE id='speech'`
	}
	if _, err := db.ExecContext(t.Context(), query); err != nil {
		t.Fatal(err)
	}
}

func hasDefaultSpeechTool(p *permittedTools) bool {
	for _, tool := range p.speechTools() {
		if tool.Name == "speech_synthesize" {
			return true
		}
	}
	return false
}

func assertSpeechVoiceSelectionSchema(t *testing.T, p *permittedTools, namedOnly bool) {
	t.Helper()
	for _, tool := range p.speechTools() {
		if tool.Name != "speech_synthesize" {
			continue
		}
		var schema struct {
			Required   []string `json:"required"`
			Properties map[string]struct {
				MinLength int `json:"minLength"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		requiresVoice := false
		for _, name := range schema.Required {
			requiresVoice = requiresVoice || name == "voice_id"
		}
		if requiresVoice != namedOnly || namedOnly && schema.Properties["voice_id"].MinLength < 1 {
			t.Fatal("discovery does not bind named-only voice selection", string(tool.InputSchema))
		}
		if namedOnly && strings.Contains(tool.Description, "Omit voice_id") {
			t.Fatal("named-only discovery advertises unavailable default", tool.Description)
		}
		return
	}
	t.Fatal("speech tool missing")
}

func TestSpeechDefaultRequiresInstalledAuthorizedConfiguredPlugin(t *testing.T) {
	for _, mode := range []string{"missing", "disabled", "misconfigured", "unauthorized", "ready"} {
		t.Run(mode, func(t *testing.T) {
			f, p := trustedAgent(t)
			setLegacyDefaultSpeechFixture(t, f.manager)
			if mode != "missing" {
				installDefaultSpeechFixture(t, f.manager, true, mode != "disabled")
				if mode == "misconfigured" || mode == "unauthorized" {
					breakDefaultSpeechFixture(t, f.manager, mode)
				}
			}
			if got := hasDefaultSpeechTool(p); got != (mode == "ready") {
				t.Fatal("default discovery used legacy settings or invalid installation", mode, got)
			}
			// Even an enabled, keyed legacy profile cannot execute the old cloud
			// provider. No real runtime is connected in these offline fixtures.
			_, err := p.generateSpeech(t.Context(), "speech_synthesize", json.RawMessage(`{"text":"synthetic default speech"}`))
			if err == nil {
				t.Fatal("offline default unexpectedly executed")
			}
			if mode == "missing" && !strings.Contains(err.Error(), "speech_plugin_migration_required") {
				t.Fatal("missing package did not explain migration", err)
			}
			if strings.Contains(err.Error(), "百炼") || strings.Contains(err.Error(), readLegacySpeechTestConfig(t, f.manager).Key) {
				t.Fatal("default fell through to legacy provider", err)
			}
			if readLegacySpeechTestConfig(t, f.manager).Key != "synthetic-legacy-key-never-dispatched" {
				t.Fatal("plugin route changed compatibility profile")
			}
		})
	}
}

func TestSpeechDefaultPluginDoesNotDependOnLegacyTTSFlagOrKey(t *testing.T) {
	f, p := trustedAgent(t)
	setLegacyDefaultSpeechFixture(t, f.manager)
	installDefaultSpeechFixture(t, f.manager, true, true)
	f.manager.mu.Lock()
	{
		legacySpeechFixture := readLegacySpeechTestConfig(t, f.manager)
		legacySpeechFixture.Key, legacySpeechFixture.TTSEnabled = "", false
		writeLegacySpeechMigrationFixture(t, f.manager, legacySpeechFixture)
	}
	f.manager.mu.Unlock()
	if !hasDefaultSpeechTool(p) {
		t.Fatal("installed default hidden by legacy TTS switch")
	}
	assertSpeechVoiceSelectionSchema(t, p, false)
	_, err := p.generateSpeech(t.Context(), "speech_synthesize", json.RawMessage(`{"text":"synthetic default speech"}`))
	if err == nil || !strings.Contains(err.Error(), "speech_plugin_runtime_unavailable") {
		t.Fatal("default did not use installed runtime", err)
	}
}

func TestSpeechDefaultMigrationPreservesNamedLibrarySelection(t *testing.T) {
	f, p, policy, _, _ := namedSpeechContractFixture(t)
	voice := policy.resource.Voice
	setLegacyDefaultSpeechFixture(t, f.manager)
	if !hasDefaultSpeechTool(p) {
		t.Fatal("installed local voice capability incorrectly required default cloud configuration")
	}
	assertSpeechVoiceSelectionSchema(t, p, true)
	_, err := p.generateSpeech(t.Context(), "speech_synthesize", json.RawMessage(`{"voice_id":"missing","text":"synthetic"}`))
	if err == nil || !strings.Contains(err.Error(), "voice_not_found_or_not_authorized") {
		t.Fatal("missing named voice fell back", err)
	}
	raw, _ := json.Marshal(map[string]string{"voice_id": voice.ID, "text": "synthetic"})
	_, err = p.generateSpeech(t.Context(), "speech_synthesize", raw)
	if err == nil || !strings.Contains(err.Error(), "named_speech_runtime_unavailable") {
		t.Fatal("known named voice did not retain library route", err)
	}
	p.trace.RequestedText = "请使用 " + voice.Name + " 说话"
	_, err = p.generateSpeech(t.Context(), "speech_synthesize", json.RawMessage(`{"text":"synthetic"}`))
	var selection *RuntimeFailure
	if !errors.As(err, &selection) || selection.Code != "named_voice_selection_required" {
		t.Fatal("requested named voice was silently substituted", err)
	}
}

// This executor models only the installed runtime boundary. It cannot issue a
// provider request. A fabricated URL must remain unverified and never download.
type defaultSpeechTestExecutor struct {
	starts, calls int
	invocation    pluginhost.Invocation
	arguments     json.RawMessage
}

func (e *defaultSpeechTestExecutor) Start(_ context.Context, pkg pluginhost.Package, grant pluginhost.Grant) (pluginhost.Session, error) {
	if pkg.ID != "speech" || grant.Account != "test" {
		return nil, errors.New("unexpected speech installation")
	}
	e.starts++
	return e, nil
}
func (*defaultSpeechTestExecutor) Health(context.Context) error { return nil }
func (*defaultSpeechTestExecutor) Close() error                 { return nil }
func (e *defaultSpeechTestExecutor) Call(_ context.Context, inv pluginhost.Invocation, capability string, raw json.RawMessage) (json.RawMessage, error) {
	if capability != "speech.synthesize" {
		return nil, errors.New("unexpected speech capability")
	}
	e.calls++
	e.invocation, e.arguments = inv, append(json.RawMessage(nil), raw...)
	return json.RawMessage(`{"ok":true,"status":"provider_accepted","provider_audio_url":"https://audio.example/forged.wav","provider_model":"synthetic-plugin-model","voice":"synthetic-plugin-voice","coverage":"provider_tts_response","untrusted_external_content":true}`), nil
}

func TestSpeechDefaultAdminTestRequiresAuthCSRFAndNotDraining(t *testing.T) {
	f := setup(t)
	setLegacyDefaultSpeechFixture(t, f.manager)
	input := map[string]string{"text": "synthetic administrator test"}
	if code, _, _ := f.request("POST", "/api/speech/test", input, true, false); code != 401 {
		t.Fatal("unauthenticated speech test", code)
	}
	f.login()
	if code, _, _ := f.request("POST", "/api/speech/test", input, true, false); code != 403 {
		t.Fatal("speech test bypassed CSRF", code)
	}
	f.manager.mu.Lock()
	f.manager.drainTicket = "synthetic-drain"
	f.manager.mu.Unlock()
	if code, _, _ := f.request("POST", "/api/speech/test", input, true, true); code != 409 {
		t.Fatal("draining speech test executed", code)
	}
	f.manager.mu.Lock()
	f.manager.drainTicket = ""
	f.manager.mu.Unlock()
	for _, text := range []string{"", " \n\t", strings.Repeat("你", 2001), "synthetic\x00"} {
		if code, _, _ := f.request("POST", "/api/speech/test", map[string]string{"text": text}, true, true); code != 400 {
			t.Fatal("invalid speech test text reached plugin", code)
		}
	}
	if code, body, _ := f.request("POST", "/api/speech/test", input, true, true); code != 409 || !strings.Contains(string(body), "speech_plugin_migration_required") {
		t.Fatalf("legacy profile used without installed plugin: %d %s", code, body)
	}
}

func TestSpeechDefaultLegacySettingsRemainCompatibilityOnly(t *testing.T) {
	f := setup(t)
	f.login()
	installDefaultSpeechFixture(t, f.manager, true, true)
	store, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.ConfigurationForExecution(t.Context(), "speech")
	if err != nil {
		t.Fatal(err)
	}
	legacy := mediaspec.Defaults()
	legacy.Key, legacy.TTSEnabled, legacy.Voice = "synthetic-legacy-only-new-key", true, "synthetic-legacy-new-voice"
	if code, body, _ := f.request("POST", "/api/speech", legacy, true, true); code != 409 || !strings.Contains(string(body), "speech_legacy_configuration_migration_required") {
		t.Fatalf("legacy compatibility writer remained active: %d %s", code, body)
	}
	after, err := store.ConfigurationForExecution(t.Context(), "speech")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("legacy settings silently overwrote installed plugin", err)
	}
	if readLegacySpeechTestConfig(t, f.manager).Key == legacy.Key {
		t.Fatal("legacy configuration write was accepted")
	}
}

func TestSpeechDefaultAdminTestUsesInstalledControllerAndRejectsUnverifiedAudio(t *testing.T) {
	f := setup(t)
	f.login()
	setLegacyDefaultSpeechFixture(t, f.manager)
	installDefaultSpeechFixture(t, f.manager, true, true)
	store, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	executor := &defaultSpeechTestExecutor{}
	controller, err := pluginruntime.NewController(store, executor)
	if err != nil {
		t.Fatal(err)
	}
	f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
	// A local administrator test does not need or impersonate a chat source,
	// an enabled conversation Agent, or permission to send a WeChat voice.
	f.manager.mu.Lock()
	f.manager.config.Enabled = false
	f.manager.config.Companion.Permissions.SendVoice = false
	{
		legacySpeechFixture := readLegacySpeechTestConfig(t, f.manager)
		legacySpeechFixture.Key, legacySpeechFixture.TTSEnabled = "", false
		writeLegacySpeechMigrationFixture(t, f.manager, legacySpeechFixture)
	}
	f.manager.mu.Unlock()
	code, body, _ := f.request("POST", "/api/speech/test", map[string]string{"text": "synthetic administrator test"}, true, true)
	if code != 502 || executor.starts != 1 || executor.calls != 1 {
		t.Fatalf("administrator test skipped installed Controller: %d %s starts=%d calls=%d", code, body, executor.starts, executor.calls)
	}
	if executor.invocation.Account != "test" || executor.invocation.Authorization == "" || executor.invocation.Purpose == "synthesize conversation speech" || executor.invocation.Thread == "filehelper" || executor.invocation.Requester == "synthetic-user" {
		t.Fatal("administrator test impersonated conversation authority", executor.invocation)
	}
	if !strings.Contains(string(executor.arguments), "synthetic administrator test") || strings.Contains(string(executor.arguments), "synthetic-legacy") || strings.Contains(string(executor.arguments), "synthetic-plugin-only-key") {
		t.Fatal("runtime arguments used legacy configuration or exposed provider key", string(executor.arguments))
	}
	jobs, err := store.Jobs(t.Context(), "speech", 0, 10)
	if err != nil || len(jobs.Items) != 1 || jobs.Items[0].State != "needs_review" {
		t.Fatal("unverified audio was not retained for review", err, jobs)
	}
	if strings.Contains(string(body), "forged.wav") {
		t.Fatal("unverified provider URL exposed in HTTP result")
	}
}

func TestSpeechDefaultAdminTestNeverFallsBackFromDisabledOrMisconfiguredPlugin(t *testing.T) {
	for _, mode := range []string{"disabled", "misconfigured", "unauthorized"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			f.login()
			setLegacyDefaultSpeechFixture(t, f.manager)
			installDefaultSpeechFixture(t, f.manager, true, mode != "disabled")
			if mode != "disabled" {
				breakDefaultSpeechFixture(t, f.manager, mode)
			}
			code, body, _ := f.request("POST", "/api/speech/test", map[string]string{"text": "synthetic administrator test"}, true, true)
			if code != 409 || strings.Contains(string(body), "百炼") || strings.Contains(string(body), "synthetic-legacy") {
				t.Fatalf("invalid plugin fell back to legacy speech: %d %s", code, body)
			}
			store, _ := f.manager.packageStore()
			jobs, err := store.Jobs(t.Context(), "speech", 0, 10)
			if err != nil || len(jobs.Items) != 0 {
				t.Fatal("rejected administrator test dispatched", jobs, err)
			}
		})
	}
}
