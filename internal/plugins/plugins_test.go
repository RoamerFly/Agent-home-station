package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)
}

func TestBridgeSingleStepTransportRendersTemplateAndReturnsRawReply(t *testing.T) {
	var gotBody map[string]any
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/create" || r.Header.Get("Authorization") != "Bearer private-token" {
			t.Fatal("bridge did not keep the fixed transport configuration")
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"finished"}`))
	}))
	defer s.Close()
	u := strings.Replace(s.URL, "127.0.0.1", "localhost", 1)
	b, err := NewExternalBridge([]Installed{{Enabled: true, Secrets: map[string]string{"TOKEN": "private-token"}, Manifest: Manifest{ID: "music", Name: "Music", Version: "1", Tools: []APITool{{Name: "make", Description: "x", InputSchema: schema(), URL: u + "/create", Headers: map[string]string{"Authorization": "Bearer {{secret.TOKEN}}"}, BodyTemplate: json.RawMessage(`{"prompt":"{{arg.text}}","count":"{{arg.count}}","fixed":true}`)}}}}})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := b.Execute(context.Background(), ExternalRequest{Phase: "call", Tool: "music__make", Arguments: json.RawMessage(`{"text":"hello","count":3}`)})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["count"] != float64(3) || gotBody["fixed"] != true {
		t.Fatalf("template types not preserved: %#v", gotBody)
	}
	if reply.ContentType != "application/json" || reply.BodyBase64 == "" {
		t.Fatalf("bridge did not preserve raw reply: %#v", reply)
	}
}

func TestBridgeBinaryResponsePreservesRawBytes(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("sound"))
	}))
	defer s.Close()
	u := strings.Replace(s.URL, "127.0.0.1", "localhost", 1)
	b, err := NewExternalBridge([]Installed{{
		Enabled:  true,
		Manifest: Manifest{ID: "voice", Name: "Voice", Version: "1", Tools: []APITool{{Name: "make", Description: "x", InputSchema: schema(), URL: u, MediaKind: "voice"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := b.Execute(context.Background(), ExternalRequest{Phase: "call", Tool: "voice__make", Arguments: json.RawMessage(`{"text":"x"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if reply.ContentType != "audio/mpeg" || reply.BodyBase64 == "" {
		t.Fatalf("bad raw binary reply: %#v", reply)
	}
}

func TestNoCrossOriginRedirectWithCredentials(t *testing.T) {
	var targetHits int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&targetHits, 1)
		if r.Header.Get("Authorization") != "" {
			t.Error("credential leaked across redirect")
		}
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer origin.Close()
	u := strings.Replace(origin.URL, "127.0.0.1", "localhost", 1)
	b, err := NewExternalBridge([]Installed{{
		Enabled: true, Secrets: map[string]string{"K": "not-for-errors"},
		Manifest: Manifest{ID: "safe", Name: "Safe", Version: "1", Tools: []APITool{{Name: "go", Description: "x", InputSchema: schema(), URL: u, Headers: map[string]string{"Authorization": "Bearer {{secret.K}}"}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Execute(context.Background(), ExternalRequest{Phase: "call", Tool: "safe__go", Arguments: json.RawMessage(`{}`)})
	if err == nil {
		t.Fatal("redirect accepted")
	}
	if targetHits != 0 {
		t.Fatal("redirect target was contacted")
	}
	if strings.Contains(err.Error(), "not-for-errors") {
		t.Fatal("secret appeared in error")
	}
}

func TestNamespaceAndURLValidation(t *testing.T) {
	bad := Manifest{ID: "bad__id", Name: "x", Version: "1"}
	if Validate(bad) == nil {
		t.Fatal("reserved plugin namespace accepted")
	}
	bad = Manifest{ID: "ok", Name: "x", Version: "1", Tools: []APITool{{Name: "tool", Description: "x", InputSchema: schema(), URL: "https://{{arg.host}}/x"}}}
	if Validate(bad) == nil {
		t.Fatal("model-directed URL accepted")
	}
	valid := Manifest{ID: "ok", Name: "x", Version: "1", Tools: []APITool{{Name: "tool", Description: "x", InputSchema: schema(), URL: "https://api.example/x"}}}
	if _, err := New([]Installed{{Enabled: true, Manifest: valid}, {Enabled: true, Manifest: valid}}); err == nil {
		t.Fatal("duplicate plugin id accepted")
	}
}

func TestPollURLValidationRejectsMissingID(t *testing.T) {
	if _, err := renderURL("https://example.test/tasks/{{result.missing}}", nil, nil, map[string]any{}); err == nil {
		t.Fatal("missing task ID accepted")
	}
	if validPoll(&PollSpec{URL: "http://remote.example/{{result.id}}", StatusPath: "s", DoneValues: []string{"done"}}) == nil {
		t.Fatal("insecure templated URL accepted")
	}
}

func TestManifestJSONContractRoundTrip(t *testing.T) {
	const descriptor = `{
      "id":"voice","name":"语音","version":"1","description":"configured",
      "tools":[{"name":"speak","description":"s","input_schema":{"type":"object"},
        "url":"https://api.example/create","method":"POST",
        "body_template":{"voice":"cute","text":"{{arg.text}}"},
        "text_path":"data.note","media_url_path":"data.audio_url","media_kind":"voice",
        "poll":{"url":"https://api.example/tasks/{{result.task_id}}","method":"GET","status_path":"data.state","done_values":["done"],"failed_values":["failed"],"media_url_path":"data.audio","text_path":"data.note","interval_seconds":2,"max_attempts":8}
      }]
    }`
	var m Manifest
	if err := json.Unmarshal([]byte(descriptor), &m); err != nil {
		t.Fatal(err)
	}
	if m.ID != "voice" || m.Tools[0].TextPath != "data.note" || m.Tools[0].MediaURLPath != "data.audio_url" || m.Tools[0].Poll.StatusPath != "data.state" || m.Tools[0].Poll.MaxAttempts != 8 {
		t.Fatalf("snake_case descriptor did not unmarshal: %#v", m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err = json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	if _, ok := wire["id"]; !ok {
		t.Fatal("manifest emitted non-contract ID field")
	}
	tool := wire["tools"].([]any)[0].(map[string]any)
	if _, ok := tool["media_url_path"]; !ok {
		t.Fatal("tool emitted non-contract media URL field")
	}
	poll := tool["poll"].(map[string]any)
	if _, ok := poll["done_values"]; !ok {
		t.Fatal("poll emitted non-contract done values field")
	}
	for _, template := range Templates() {
		b, err := json.Marshal(template)
		if err != nil {
			t.Fatal(err)
		}
		var parsed Manifest
		if err = json.Unmarshal(b, &parsed); err != nil {
			t.Fatal(err)
		}
		if parsed.ID == "" {
			t.Fatal("template did not emit id")
		}
	}
}
