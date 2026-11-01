package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/pluginruntime"
)

func adminSpeechFixture(t *testing.T) (*webFixture, context.Context, *speechPluginPolicy, *bool) {
	t.Helper()
	f, _, _ := speechPluginFixture(t)
	f.manager.mu.Lock()
	f.manager.config.Enabled = false
	f.manager.config.Companion.Permissions.SendVoice = false
	f.manager.mu.Unlock()
	valid := true
	authority := &speechAdminTestAuthority{Account: "test", Session: "synthetic-session-fingerprint", RequestID: "synthetic-admin-request", Check: func(ctx context.Context) error {
		if !valid {
			return errors.New("synthetic session expired")
		}
		return ctx.Err()
	}}
	ctx := context.WithValue(t.Context(), speechAdminTestContextKey{}, authority)
	policy, err := f.manager.newAdminSpeechPluginPolicy(ctx, "合成管理员测试")
	if err != nil {
		t.Fatal(err)
	}
	return f, ctx, policy, &valid
}

func TestAdminSpeechPluginAuthorityAndProviderBindings(t *testing.T) {
	_, ctx, policy, valid := adminSpeechFixture(t)
	job, capability := speechPluginJob(policy)
	if policy.p != nil || job.Context.Thread != "host:admin-speech-test" || job.Context.Requester != "administrator" || job.Context.Purpose != "admin speech synthesis test" || len(job.Context.SourceVersions) != 0 || job.Context.EvidenceEpoch != "" {
		t.Fatal("management test impersonated a chat request", job.Context)
	}
	calls := 0
	policy.post = func(_ context.Context, _ string, headers map[string]string, body json.RawMessage, _ int64, timeout time.Duration) (json.RawMessage, error) {
		calls++
		if headers["Authorization"] != "Bearer synthetic-private-key" || !bytes.Contains(body, []byte(`"voice":"synthetic-voice"`)) || timeout != 150*time.Second {
			t.Fatal("management request did not use bound plugin configuration")
		}
		return json.RawMessage(`{"model":"synthetic-tts","output":{"audio":{"url":"https://audio.example/admin.wav?token=synthetic","voice":"synthetic-voice"}}}`), nil
	}
	request := speechPluginRequest(policy)
	if _, err := policy.Service(ctx, job, capability, "wechat/model_request", request); err != nil {
		t.Fatal(err)
	}
	result := json.RawMessage(`{"ok":true,"status":"provider_accepted","provider_audio_url":"https://audio.example/admin.wav?token=synthetic","provider_model":"synthetic-tts","voice":"synthetic-voice","coverage":"provider_tts_response","untrusted_external_content":true}`)
	verified, err := policy.Validate(ctx, job, capability, result)
	if err != nil || bytes.Contains(verified.Result, []byte("token=synthetic")) || !bytes.Contains(verified.Private, []byte("token=synthetic")) {
		t.Fatal("management result lost provider provenance or leaked private URL", err)
	}
	if _, err = policy.Service(ctx, job, capability, "wechat/model_request", request); err == nil || calls != 1 {
		t.Fatal("management provider request repeated", err, calls)
	}
	*valid = false
	if _, err = policy.Validate(ctx, job, capability, result); err == nil {
		t.Fatal("expired administrator session retained access")
	}
}

func TestAdminSpeechPluginRejectsMissingAuthorityAndChangedSession(t *testing.T) {
	f, ctx, policy, valid := adminSpeechFixture(t)
	if _, err := f.manager.synthesizePluginSpeechTest(t.Context(), "test"); err == nil {
		t.Fatal("ordinary context acquired administrator authority")
	}
	job, capability := speechPluginJob(policy)
	policy.post = func(context.Context, string, map[string]string, json.RawMessage, int64, time.Duration) (json.RawMessage, error) {
		*valid = false
		return json.RawMessage(`{"output":{"audio":{"url":"https://audio.example/test.wav"}}}`), nil
	}
	if _, err := policy.Service(ctx, job, capability, "wechat/model_request", speechPluginRequest(policy)); err == nil {
		t.Fatal("authority revoked during the provider call was accepted")
	}
}

func TestLiveAdminSpeechPluginIsolationContract(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in isolated plugin with synthetic provider only")
	}
	f, ctx, policy, _ := adminSpeechFixture(t)
	helper, err := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := pluginruntime.NewController(policy.store, pluginruntime.InstalledExecutor(policy.store, pluginruntime.Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}))
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
	calls := 0
	policy.post = func(context.Context, string, map[string]string, json.RawMessage, int64, time.Duration) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"model":"synthetic-tts","output":{"audio":{"url":"https://audio.example/admin-isolated.wav"}}}`), nil
	}
	result, err := policy.synthesize(ctx)
	if err != nil || result.URL != "https://audio.example/admin-isolated.wav" || calls != 1 {
		t.Fatal("isolated administrator workflow failed", result, err, calls)
	}
	jobs, err := policy.store.Jobs(ctx, "speech", 0, 10)
	if err != nil || len(jobs.Items) != 1 || jobs.Items[0].Context.Purpose != "admin speech synthesis test" || jobs.Items[0].State != "completed" || strings.Contains(string(jobs.Items[0].Result), "audio.example") {
		t.Fatal("management job was misclassified or exposed private URL", err, jobs)
	}
}
