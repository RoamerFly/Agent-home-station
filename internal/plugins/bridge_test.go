package plugins

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExternalBridgeProjectsCredentialFreeProtocolAndSingleCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/speak" || r.Header.Get("Authorization") != "Bearer synthetic-secret" {
			t.Fatal("bridge did not keep fixed legacy request configuration")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"text":"synthetic"}}`))
	}))
	defer server.Close()
	bridge, err := NewExternalBridge([]Installed{{Enabled: true, Secrets: map[string]string{"KEY": "synthetic-secret"}, Manifest: Manifest{ID: "voice", Name: "voice", Version: "1", Tools: []APITool{{Name: "speak", Description: "Synthetic", InputSchema: json.RawMessage(`{"type":"object"}`), URL: server.URL + "/speak", Headers: map[string]string{"Authorization": "Bearer {{secret.KEY}}"}, BodyTemplate: json.RawMessage(`{"text":"{{arg.text}}"}`), TextPath: "result.text"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	protocol := bridge.Protocol()
	if len(protocol.Tools) != 1 || protocol.Tools[0].Name != "voice__speak" || protocol.Tools[0].Fingerprint == "" {
		t.Fatalf("unexpected protocol: %#v", protocol)
	}
	encoded, _ := json.Marshal(protocol)
	if strings.Contains(string(encoded), "synthetic-secret") || strings.Contains(string(encoded), server.URL) || strings.Contains(string(encoded), "Authorization") {
		t.Fatalf("protocol leaked host configuration: %s", encoded)
	}
	reply, err := bridge.Execute(context.Background(), ExternalRequest{Phase: "call", Tool: "voice__speak", Arguments: json.RawMessage(`{"text":"hello"}`)})
	if err != nil || reply.ContentType != "application/json" || reply.BodyBase64 == "" {
		t.Fatalf("unexpected bridge reply: %#v %v", reply, err)
	}
}

func TestExternalBridgePreservesJSONNumberLexeme(t *testing.T) {
	var received string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"text":"ok"}}`))
	}))
	defer server.Close()
	item := Installed{
		Enabled: true,
		Manifest: Manifest{ID: "exact", Name: "exact", Version: "1", Tools: []APITool{
			{Name: "call", Description: "x", InputSchema: json.RawMessage(`{"type":"object"}`), URL: server.URL, BodyTemplate: json.RawMessage(`{"value":"{{arg.value}}"}`), TextPath: "result.text"},
		}},
	}
	bridge, err := NewExternalBridge([]Installed{item})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bridge.Execute(context.Background(), ExternalRequest{Phase: "call", Tool: "exact__call", Arguments: json.RawMessage(`{"value":9007199254740993}`)}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(received, "9007199254740993") {
		t.Fatalf("numeric lexeme changed: %s", received)
	}
	if _, err = bridge.Execute(context.Background(), ExternalRequest{Phase: "call", Tool: "exact__call", Arguments: json.RawMessage(`{} {}`)}); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}

func TestExternalBridgeRejectsBackgroundCallPhase(t *testing.T) {
	item := Installed{
		Enabled: true,
		Manifest: Manifest{ID: "jobs", Name: "jobs", Version: "1", Tools: []APITool{
			{Name: "start", Description: "x", InputSchema: json.RawMessage(`{"type":"object"}`), URL: "https://example.test/jobs", Job: &JobSpec{IDPath: "id"}, Poll: &PollSpec{URL: "https://example.test/jobs/{{result.id}}", StatusPath: "state", DoneValues: []string{"done"}}},
		}},
	}
	bridge, err := NewExternalBridge([]Installed{item})
	if err != nil {
		t.Fatal(err)
	}
	if len(bridge.Protocol().Tools) != 0 {
		t.Fatal("async declaration entered synchronous protocol")
	}
	if _, err = bridge.Execute(context.Background(), ExternalRequest{Phase: "call", Tool: "jobs__start", Arguments: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("background tool entered synchronous bridge")
	}
}
