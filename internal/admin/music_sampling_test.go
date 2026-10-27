package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"wechat-bot/internal/companion"
)

func TestMusicSamplingDiscoveryForwardingPersistenceAndIdentity(t *testing.T) {
	f := setup(t)
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "sampling"})
	posts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Write([]byte(`{"ready":true,"bridge_version":8}`))
			return
		}
		if r.URL.Path == "/parameters" {
			w.Write([]byte(`{"modes":{"original":{"native_limits_verified":true}}}`))
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		posts++
		sampling := body["sampling"].(map[string]any)
		semantic := sampling["semantic"].(map[string]any)
		if semantic["temperature"] != float64(0) || body["planning_mode"] != "melody" {
			t.Error("explicit zero/planning lost", body)
		}
		sampling["render"].(map[string]any)["denoise"] = float64(1) // Simulate resolved native default.
		json.NewEncoder(w).Encode(map[string]any{"id": "sampling-" + r.Header.Get("Idempotency-Key")[:8], "state": "queued", "effective_parameters": map[string]any{"sampling": sampling, "planning_mode": "melody"}})
	}))
	defer upstream.Close()
	f.manager.speech.YuEEnabled, f.manager.speech.YuEEndpoint, f.manager.speech.YuEToken = true, upstream.URL, "synthetic-token"
	result, err := p.callYuE(context.Background(), "music_sampling_options", json.RawMessage(`{}`))
	if err != nil || !strings.Contains(result, "native_limits_verified") || posts != 0 {
		t.Fatal(result, err)
	}
	for _, steps := range []int{45, 45, 77} {
		raw, _ := json.Marshal(map[string]any{"title": "采样实验", "style": "folk", "lyrics": "[Verse]\nsynthetic", "seed": 123, "planning_mode": "melody", "sampling": map[string]any{"semantic": map[string]any{"temperature": 0}, "render": map[string]any{"steps": steps}}})
		result, err = p.callYuE(context.Background(), "music_local_start", raw)
		if err != nil || !strings.Contains(result, `"denoise":1`) {
			t.Fatal(result, err)
		}
	}
	if posts != 2 {
		t.Fatal("retry replayed or changed sampling silently reused", posts)
	}
	recovered, err := newAgentControl(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	tasks := recovered.list("test", "filehelper")
	if len(tasks) != 2 {
		t.Fatal("missing task recipes", len(tasks))
	}
	for _, task := range tasks {
		if task.MusicSpec.PlanningMode != "melody" || task.MusicSpec.Sampling["semantic"]["temperature"] != float64(0) || task.MusicSpec.Sampling["render"]["denoise"] != float64(1) {
			t.Fatal("effective recipe lost", task.MusicSpec)
		}
	}
	found := false
	for _, tool := range p.yueTools() {
		if tool.Name == "music_local_start" {
			data, _ := json.Marshal(tool)
			found = strings.Contains(string(data), "sampling") && strings.Contains(string(data), "sampler_name")
		}
	}
	if !found {
		t.Fatal("parameters not discoverable in model function schema")
	}
}

func TestMusicSamplingNativeFailureIsActionableAndNotSuccess(t *testing.T) {
	f := setup(t)
	p, _, _ := f.manager.toolAccess()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":"sampling_outside_native_spec:render.steps"}`))
	}))
	defer upstream.Close()
	f.manager.speech.YuEEnabled, f.manager.speech.YuEEndpoint, f.manager.speech.YuEToken = true, upstream.URL, "synthetic-token"
	_, _, err := p.yueRequest(context.Background(), "POST", "/jobs", map[string]any{}, "test")
	if err == nil || !strings.Contains(err.Error(), "render.steps") || !strings.Contains(err.Error(), "music_sampling_options") {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"sampling":{"semantic":{"top_kk":3}}}`, `{"sampling":{"render":{"steps":true}}}`, `{"sampling":{"unknown":{}}}`} {
		if _, err := p.callYuE(context.Background(), "music_local_start", json.RawMessage(raw)); err == nil {
			t.Fatal("invalid sampling accepted", raw)
		}
	}
}
