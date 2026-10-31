package pluginruntime

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

	"wechat-bot/internal/pluginhost"
)

// Synthetic policy: deliberately not wired into production or represented as
// the completed account/asset adapter. It exercises the real proxy and process.
type offlineMediaPolicy struct {
	offlineWebPolicy
	store *pluginhost.Store
}

func (p *offlineMediaPolicy) Service(ctx context.Context, j pluginhost.Job, cap pluginhost.Capability, method string, raw json.RawMessage) (json.RawMessage, error) {
	if method != "wechat/model_request" {
		return nil, errors.New("unexpected service")
	}
	materialize := func(_ context.Context, route string, body json.RawMessage) (json.RawMessage, error) {
		var obj map[string]any
		if route != "vision" || json.Unmarshal(body, &obj) != nil || obj["model"] != "synthetic-model" || bytes.Contains(body, []byte("synthetic-private-key")) {
			return nil, errors.New("model configuration changed")
		}
		messages, ok := obj["messages"].([]any)
		if !ok || len(messages) != 2 {
			return nil, errors.New("messages invalid")
		}
		user := messages[1].(map[string]any)
		parts := user["content"].([]any)
		if len(parts) != 2 {
			return nil, errors.New("image count changed")
		}
		image := parts[1].(map[string]any)["image_url"].(map[string]any)
		marker := image["url"].(map[string]any)["$media_ref"].(map[string]any)
		if marker["media_ref"] != "frame_1" || marker["sha256"] != strings.Repeat("a", 64) || marker["version"] != float64(2) || marker["mime"] != "image/png" {
			return nil, errors.New("unauthorized synthetic media")
		}
		// Synthetic bytes only. This proves reference replacement, not image decode.
		image["url"] = "data:image/png;base64,c3ludGhldGlj"
		return json.Marshal(obj)
	}
	return ProxyModelRoute(ctx, p.store, j, cap, raw, materialize, func(_ context.Context, endpoint string, headers map[string]string, body json.RawMessage, _ int64, _ time.Duration) (json.RawMessage, error) {
		if endpoint != "https://provider.example/chat/completions" || headers["Authorization"] != "Bearer synthetic-private-key" || bytes.Contains(body, []byte("$media_ref")) || !bytes.Contains(body, []byte("data:image/png;base64,")) {
			return nil, errors.New("model transport mismatch")
		}
		p.calls++
		if p.revokeAfterRead {
			p.revoked = true
		}
		return json.RawMessage(`{"choices":[{"message":{"content":"合成画面描述","reasoning_content":"private reasoning"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110}}`), nil
	})
}
func (p *offlineMediaPolicy) Validate(_ context.Context, _ pluginhost.Job, _ pluginhost.Capability, raw json.RawMessage) (VerifiedResult, error) {
	var result struct {
		OK          bool   `json:"ok"`
		Observation string `json:"observation"`
		Coverage    string `json:"coverage"`
		Audio       bool   `json:"audio_observed"`
	}
	if json.Unmarshal(raw, &result) != nil || !result.OK || result.Observation != "合成画面描述" || result.Coverage != "provided_images_only" || result.Audio || bytes.Contains(raw, []byte("private reasoning")) {
		return VerifiedResult{}, errors.New("invalid synthetic observation: " + string(raw))
	}
	return VerifiedResult{Result: raw}, nil
}

func TestLiveMediaPluginScopedModelProxy(t *testing.T) {
	python := os.Getenv("WECHAT_PLUGIN_TEST_PYTHON")
	if python == "" {
		t.Skip("opt-in real isolation; synthetic provider, no paid calls")
	}
	python, err := filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	helper, _ := filepath.Abs("../../scripts/windows_plugin_host.pyw")
	store, err := pluginhost.OpenStore(t.TempDir(), "a")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	prepared, err := store.Prepare(ctx, "../../plugins/media-understanding")
	if err != nil {
		t.Fatal(err)
	}
	// Install the checked-in ready package unchanged.  The transcription route
	// is optional, so this visual-only synthetic configuration proves that an
	// official install need not carry unrelated ASR credentials.
	p := prepared.Package
	i, err := store.Install(ctx, prepared.Token, prepared.Digest, p.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.SaveConfiguration(ctx, p.ID, pluginhost.ConfigUpdate{Values: map[string]json.RawMessage{"vision_endpoint": json.RawMessage(`"https://provider.example/chat/completions"`), "vision_model": json.RawMessage(`"synthetic-model"`)}, Secrets: map[string]string{"vision_key": "synthetic-private-key"}})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := NewController(store, InstalledExecutor(store, Options{Python: python, Pythonw: filepath.Join(filepath.Dir(python), "pythonw.exe"), WindowsHelper: helper, RuntimeRoots: []string{filepath.Dir(python)}}))
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	if err = controller.Start(ctx, p.ID, i.Revision); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"images": []any{map[string]any{"media_ref": "frame_1", "sha256": strings.Repeat("a", 64), "version": 2, "mime": "image/png"}}})
	request := CallRequest{Plugin: p.ID, Capability: "media.observe", Context: invocation(), Arguments: args}
	policy := &offlineMediaPolicy{store: store}
	first, err := controller.Invoke(ctx, request, policy)
	if err != nil || first.Job.State != "completed" || policy.calls != 1 {
		t.Fatal("isolated media call", first.Job.State, err, policy.calls)
	}
	again, err := controller.Invoke(ctx, request, policy)
	if err != nil || !again.Reused || policy.calls != 1 {
		t.Fatal("model call replayed", err)
	}
	request.Context.RequestKey = "wrong-source"
	request.Arguments = bytes.Replace(args, []byte("frame_1"), []byte("foreign_frame"), 1)
	blocked, err := controller.Invoke(ctx, request, policy)
	if err == nil || blocked.Job.State != "needs_review" || policy.calls != 1 {
		t.Fatal("unbound source reached provider", err, policy.calls)
	}
	request.Context.RequestKey = "revoked"
	request.Arguments = args
	revoke := &offlineMediaPolicy{store: store, offlineWebPolicy: offlineWebPolicy{revokeAfterRead: true}}
	blocked, err = controller.Invoke(ctx, request, revoke)
	if err == nil || blocked.Job.State != "needs_review" || len(blocked.Job.Result) != 0 || revoke.calls != 1 {
		t.Fatal("revoked observation accepted", err)
	}
}

func TestModelProxyRejectsAmbiguousOrUnboundEnvelope(t *testing.T) {
	send := func(context.Context, string, map[string]string, json.RawMessage, int64, time.Duration) (json.RawMessage, error) {
		t.Fatal("unexpected provider call")
		return nil, nil
	}
	for _, raw := range []string{
		`{"route":"vision","route":"other","body":{},"max_bytes":100,"timeout_seconds":1}`,
		`{"route":"vision","body":{"model":"a","model":"b"},"max_bytes":100,"timeout_seconds":1}`,
		`{"route":"vision","body":{},"endpoint":"https://foreign.example","max_bytes":100,"timeout_seconds":1}`,
	} {
		if _, err := ProxyModelRoute(t.Context(), nil, pluginhost.Job{}, pluginhost.Capability{}, json.RawMessage(raw), func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
			t.Fatal("invalid envelope materialized")
			return nil, nil
		}, send); err == nil {
			t.Fatal("ambiguous model request accepted")
		}
	}
	if _, err := ProxyModelRoute(t.Context(), nil, pluginhost.Job{}, pluginhost.Capability{}, json.RawMessage(`{}`), nil, send); err == nil {
		t.Fatal("missing source policy accepted")
	}
}
