package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"wechat-bot/internal/bailian"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

// requireLiveSpeechPluginRuntime validates only the administrator-provided
// isolated runtime needed by the opt-in live tests. It deliberately runs before
// any provider request, so an incomplete local deployment skips rather than
// falling back to the removed direct Bailian client path.
func requireLiveSpeechPluginRuntime(t *testing.T) pluginruntime.Options {
	t.Helper()
	options, err := runtimeDeploymentOptions()
	if err != nil {
		t.Skipf("live speech plugin runtime is not configured: %v", err)
	}
	if options.Python == "" || len(options.RuntimeRoots) == 0 {
		t.Skip("live speech plugin runtime is not configured")
	}
	for _, path := range []string{options.Python} {
		info, statErr := os.Stat(path)
		if statErr != nil || !info.Mode().IsRegular() {
			t.Skip("live speech plugin Python runtime is unavailable")
		}
	}
	if runtime.GOOS == "windows" {
		for _, path := range []string{options.Pythonw, options.WindowsHelper} {
			info, statErr := os.Stat(path)
			if path == "" || statErr != nil || !info.Mode().IsRegular() {
				t.Skip("live speech plugin Windows isolation helper is unavailable")
			}
		}
	} else {
		if _, lookErr := exec.LookPath("bwrap"); lookErr != nil {
			t.Skip("live speech plugin requires bwrap isolation")
		}
	}
	return options
}

func liveSpeechInstruction(t *testing.T, configured, requested string) string {
	t.Helper()
	if requested == "" || requested == configured {
		return configured
	}
	if configured == "" {
		if utf8.RuneCountInString(requested) > 1500 {
			t.Fatal("live speech instruction exceeds official plugin configuration limit")
		}
		return requested
	}
	combined := configured + "\n本次表达要求（保持上述角色声线，只调整语气与节奏）：" + requested
	if utf8.RuneCountInString(combined) > 1500 {
		t.Fatal("combined live speech instruction exceeds official plugin configuration limit")
	}
	return combined
}

// livePluginSynthesize is deliberately test-only. It uses the exact installed
// official package and its host-bound transport, but it neither stages media
// nor calls any native WeChat API. The caller's opt-in test guard remains the
// sole authorization for a billable provider request.
func livePluginSynthesize(t *testing.T, ctx context.Context, cfg bailian.Config, text, instruction string) (string, error) {
	t.Helper()
	options := requireLiveSpeechPluginRuntime(t)
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > 2000 {
		return "", errors.New("invalid live speech text")
	}
	endpoint := strings.TrimRight(cfg.Endpoint, "/") + "/api/v1/services/audio/tts/SpeechSynthesizer"
	if cfg.Key == "" || cfg.TTSModel == "" || cfg.Voice == "" {
		return "", errors.New("live speech provider configuration incomplete")
	}

	f := setup(t)
	store, err := f.manager.packageStore()
	if err != nil {
		return "", err
	}
	prepared, err := store.Prepare(ctx, "../../plugins/speech")
	if err != nil {
		return "", err
	}
	installed, err := store.Install(ctx, prepared.Token, prepared.Digest, prepared.Package.Permissions)
	if err != nil {
		return "", err
	}
	values := map[string]json.RawMessage{}
	for key, value := range map[string]string{
		"tts_endpoint":        endpoint,
		"tts_model":           cfg.TTSModel,
		"default_voice":       cfg.Voice,
		"default_instruction": liveSpeechInstruction(t, cfg.Instruction, instruction),
	} {
		raw, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return "", marshalErr
		}
		values[key] = raw
	}
	if _, err = store.SaveConfiguration(ctx, "speech", pluginhost.ConfigUpdate{Values: values, Secrets: map[string]string{"tts_key": cfg.Key}}); err != nil {
		return "", err
	}
	if _, err = store.SetEnabled(ctx, "speech", installed.Revision, true); err != nil {
		return "", err
	}
	controller, err := pluginruntime.NewController(store, pluginruntime.InstalledExecutor(store, options))
	if err != nil {
		return "", err
	}
	f.manager.packageMu.Lock()
	f.manager.packageRuntime = controller
	f.manager.packageRuntimeReady = true
	f.manager.packageMu.Unlock()

	authority := &speechAdminTestAuthority{
		Account:   f.manager.native.Account(),
		Session:   "live-test-local-administrator",
		RequestID: "live-speech-plugin-test",
		Check: func(ctx context.Context) error {
			return ctx.Err()
		},
	}
	ctx = context.WithValue(ctx, speechAdminTestContextKey{}, authority)
	policy, err := f.manager.newAdminSpeechPluginPolicy(ctx, text)
	if err != nil {
		return "", err
	}
	result, err := policy.synthesize(ctx)
	if err != nil {
		return "", err
	}
	if err = authority.valid(ctx, f.manager); err != nil {
		return "", err
	}
	return result.URL, nil
}
