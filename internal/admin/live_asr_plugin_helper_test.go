package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/mediaspec"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

type liveASRPlugin struct {
	f           *webFixture
	server      *Server
	request     *http.Request
	sessionID   string
	sessionHash string
}

// Called only after each live test's explicit opt-in guard, and before its
// first billable clone/TTS/ASR request. Starting the official isolated runtime
// performs initialization/health only; no model or native WeChat call occurs.
func prepareLiveASRPlugin(t *testing.T, cfg mediaspec.Config) *liveASRPlugin {
	t.Helper()
	if os.Getenv("WECHAT_REFERENCE_ASR_LIVE") != "1" && os.Getenv("WECHAT_COVER_OUTPUT_LIVE") != "1" && os.Getenv("WECHAT_VOICE_CLONE_LIVE") != "1" && os.Getenv("WECHAT_SPEECH_CONTEXT_LIVE") != "1" {
		t.Skip("explicit opt-in is required for live media transcription")
	}
	options := requireLiveSpeechPluginRuntime(t)
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("live media transcription requires FFmpeg")
	}
	if cfg.Key == "" || cfg.ASRModel == "" {
		t.Skip("live media transcription provider configuration is incomplete")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal("live media transcription provider configuration is invalid")
	}
	f := setup(t)
	f.login()
	store, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	prepared, err := store.Prepare(ctx, "../../plugins/media-understanding")
	if err != nil {
		t.Fatal("official media package validation failed", err)
	}
	installed, err := store.Install(ctx, prepared.Token, prepared.Digest, prepared.Package.Permissions)
	if err != nil {
		t.Fatal("official media package installation failed", err)
	}
	values := map[string]json.RawMessage{}
	for name, value := range map[string]string{
		"asr_backend":  "remote",
		"asr_endpoint": strings.TrimRight(cfg.Endpoint, "/") + "/api/v1/services/aigc/multimodal-generation/generation",
		"asr_model":    cfg.ASRModel,
	} {
		values[name], err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.SaveConfiguration(ctx, installed.Package.ID, pluginhost.ConfigUpdate{Values: values, Secrets: map[string]string{"asr_key": cfg.Key}}); err != nil {
		t.Fatal("live media transcription configuration failed", err)
	}
	controller, err := pluginruntime.NewController(store, pluginruntime.InstalledExecutor(store, options))
	if err != nil {
		t.Fatal(err)
	}
	f.manager.packageMu.Lock()
	f.manager.packageRuntime, f.manager.packageRuntimeReady = controller, true
	f.manager.packageMu.Unlock()
	if err := controller.Start(ctx, installed.Package.ID, installed.Revision); err != nil {
		t.Skip("official media transcription isolation preflight failed; no provider request was made")
	}
	request, err := http.NewRequest("POST", f.server.URL+"/api/speech/test", nil)
	if err != nil {
		t.Fatal(err)
	}
	baseURL, err := url.Parse(f.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range f.client.Jar.Cookies(baseURL) {
		request.AddCookie(cookie)
	}
	server := f.manager.accounts.root
	sessionID, _, ok := server.session(request)
	if !ok {
		t.Fatal("live media transcription administrator session unavailable")
	}
	hash := sha256.Sum256([]byte(sessionID))
	return &liveASRPlugin{f: f, server: server, request: request, sessionID: sessionID, sessionHash: hex.EncodeToString(hash[:])}
}

// The selected file is normalized locally and bound by WAV hash to a real
// administrator session. No received-message identity or chat source is made up.
func (a *liveASRPlugin) transcribe(ctx context.Context, data []byte, sourceID string) (string, error) {
	if a == nil || sourceID == "" {
		return "", errors.New("live_asr_file_origin_required")
	}
	wav, err := normalizeSpeech(ctx, a.f.dir, data)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(wav)
	wavHash := hex.EncodeToString(hash[:])
	account := a.f.manager.native.Account()
	check := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, _, valid := a.server.session(a.request)
		if !valid || current != a.sessionID || a.f.manager.native.Account() != account || a.f.manager.draining() || sha256.Sum256(wav) != hash {
			return errors.New("live_asr_administrator_origin_changed")
		}
		return nil
	}
	invocation := pluginhost.Invocation{Account: account, Thread: "host:admin-speech-test", Requester: "administrator", Purpose: "admin speech ASR test", RequestKey: "live-file-asr-" + agentID(a.sessionHash, sourceID, wavHash), Authorization: agentID(account, a.sessionHash, sourceID, wavHash), SourceVersions: map[string]int{"wav:" + wavHash: 1}}
	policy, err := a.f.manager.newBoundMediaTranscriptionPolicy(ctx, wav, invocation, 1, check)
	if err != nil {
		return "", err
	}
	text, err := policy.transcribe(ctx)
	if err != nil {
		return "", err
	}
	if err := check(ctx); err != nil {
		return "", err
	}
	return text, nil
}
