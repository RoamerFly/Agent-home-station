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
			{Name: "start", Description: "x", InputSchema: json.RawMessage(`{"type":"object"}`), URL: "https://example.test/jobs", TimeoutSeconds: 60, Job: &JobSpec{IDPath: "id", DeadlineSeconds: 1800, MaxPolls: 9}, Poll: &PollSpec{URL: "https://example.test/jobs/{{result.id}}", StatusPath: "state", DoneValues: []string{"done"}, IntervalSeconds: 2, MaxAttempts: 12}},
		}},
	}
	bridge, err := NewExternalBridge([]Installed{item})
	if err != nil {
		t.Fatal(err)
	}
	if len(bridge.Protocol().Tools) != 0 {
		t.Fatal("async declaration entered synchronous protocol")
	}
	job, err := bridge.JobProtocol("jobs__start")
	if err != nil || job.Tool.Job == nil || job.Tool.Poll == nil || job.Tool.Fingerprint == "" {
		t.Fatalf("missing credential-free job projection: %#v %v", job, err)
	}
	encoded, _ := json.Marshal(job)
	for _, field := range []string{"interval_seconds", "max_attempts", "timeout_seconds"} {
		if strings.Contains(string(encoded), field) {
			t.Fatal("host scheduling settings entered the strict job parser contract", field)
		}
	}
	if interval, deadline, polls := bridge.runtime.JobLimits("jobs__start"); interval != 2 || deadline != 1800 || polls != 9 {
		t.Fatal("job projection changed the actual host scheduling settings", interval, deadline, polls)
	}
	if _, err = bridge.Execute(context.Background(), ExternalRequest{Phase: "call", Tool: "jobs__start", Arguments: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("background tool entered synchronous bridge")
	}
}

func TestExternalBridgeJobPhasesKeepFrozenTicketAndCredentialBoundary(t *testing.T) {
	creates, polls, cancels := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-plugin-key" {
			t.Fatal("missing fixed credential")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /jobs":
			creates++
			_, _ = w.Write([]byte(`{"task":{"id":"job-1"},"state":"queued"}`))
		case "GET /jobs/job-1":
			polls++
			_, _ = w.Write([]byte(`{"state":"done","text":"ok"}`))
		case "POST /jobs/job-1/cancel":
			cancels++
			_, _ = w.Write([]byte(`{"accepted":true}`))
		default:
			t.Fatalf("unexpected phase %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	bridge, err := NewExternalBridge([]Installed{{Manifest: backgroundManifest(server.URL), Enabled: true, Secrets: map[string]string{"KEY": "synthetic-plugin-key"}}})
	if err != nil {
		t.Fatal(err)
	}
	ticket, _ := bridge.runtime.NewJobTicket("creative__create")
	if _, err = bridge.Execute(context.Background(), ExternalRequest{Phase: "start", Tool: ticket.Tool, Ticket: ticket, Arguments: json.RawMessage(`{"prompt":"x"}`)}); err != nil {
		t.Fatal(err)
	}
	ticket.RemoteID, ticket.State = "job-1", "waiting"
	stored, _ := json.Marshal(ticket)
	var reloaded JobTicket
	_ = json.Unmarshal(stored, &reloaded)
	if _, err = bridge.Execute(context.Background(), ExternalRequest{Phase: "poll", Tool: reloaded.Tool, Ticket: reloaded, Arguments: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err = bridge.Execute(context.Background(), ExternalRequest{Phase: "cancel", Tool: reloaded.Tool, Ticket: reloaded, Arguments: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if creates != 1 || polls != 1 || cancels != 1 {
		t.Fatalf("phase counts %d/%d/%d", creates, polls, cancels)
	}
	if _, err = bridge.Execute(context.Background(), ExternalRequest{Phase: "start", Tool: reloaded.Tool, Ticket: reloaded, Arguments: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("replayed start accepted")
	}
	changed := backgroundManifest(server.URL + "/other")
	other, _ := NewExternalBridge([]Installed{{Manifest: changed, Enabled: true, Secrets: map[string]string{"KEY": "synthetic-plugin-key"}}})
	if _, err = other.Execute(context.Background(), ExternalRequest{Phase: "poll", Tool: reloaded.Tool, Ticket: reloaded, Arguments: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("frozen ticket rebound")
	}
}

func TestExternalBridgeJobPollDoesNotForwardCreationCredentialsAcrossOrigins(t *testing.T) {
	seen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = true
		if r.Header.Get("Authorization") != "" || r.URL.Path != "/jobs/job-1" {
			t.Error("cross-origin poll received a creation credential or changed ID")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"done","text":"synthetic"}`))
	}))
	defer server.Close()
	manifest := backgroundManifest("https://example.test")
	manifest.Tools[0].Poll.URL = server.URL + "/jobs/{{result.task.id}}"
	bridge, err := NewExternalBridge([]Installed{{Enabled: true, Manifest: manifest, Secrets: map[string]string{"KEY": "synthetic-plugin-key"}}})
	if err != nil {
		t.Fatal(err)
	}
	ticket, _ := bridge.runtime.NewJobTicket("creative__create")
	ticket.RemoteID, ticket.State = "job-1", "waiting"
	if _, err := bridge.Execute(t.Context(), ExternalRequest{Phase: "poll", Tool: ticket.Tool, Ticket: ticket}); err != nil || !seen {
		t.Fatal("original poll was not performed", err)
	}
}

func TestExternalBridgePollWithoutJobUsesFrozenInitialResponseOnce(t *testing.T) {
	create, query := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/create":
			create++
			_, _ = w.Write([]byte(`{"task":{"id":9007199254740993},"state":"queued"}`))
		case "/tasks/9007199254740993":
			query++
			if r.Header.Get("Authorization") != "Bearer synthetic-key" {
				t.Fatal("same-origin poll did not retain fixed credential")
			}
			_, _ = w.Write([]byte(`{"state":"done","result":{"text":"ok"}}`))
		default:
			t.Fatalf("unexpected URL %q", r.URL.Path)
		}
	}))
	defer server.Close()
	manifest := Manifest{ID: "short", Name: "short", Version: "1", Tools: []APITool{{
		Name: "create", Description: "x", InputSchema: json.RawMessage(`{"type":"object"}`), URL: server.URL + "/create", Method: http.MethodPost,
		Headers: map[string]string{"Authorization": "Bearer {{secret.KEY}}"}, BodyTemplate: json.RawMessage(`{"prompt":"{{arg.prompt}}"}`),
		Poll: &PollSpec{URL: server.URL + "/tasks/{{result.task.id}}", StatusPath: "state", DoneValues: []string{"done"}, IntervalSeconds: 2, MaxAttempts: 12, TextPath: "result.text"},
	}}}
	bridge, err := NewExternalBridge([]Installed{{Enabled: true, Manifest: manifest, Secrets: map[string]string{"KEY": "synthetic-key"}}})
	if err != nil {
		t.Fatal(err)
	}
	protocol, err := bridge.PollProtocol("short__create")
	if err != nil || protocol.Tool.Job != nil || protocol.Tool.Poll == nil || protocol.Tool.Poll.IntervalSeconds != 2 || protocol.Tool.Poll.MaxAttempts != 12 {
		t.Fatalf("bad projection %#v %v", protocol, err)
	}
	interval, attempts, timeout, err := bridge.PollLimits("short__create")
	if err != nil || interval != 2 || attempts != 12 || timeout != 60 {
		t.Fatalf("bad poll limits %d/%d/%d: %v", interval, attempts, timeout, err)
	}
	encoded, _ := json.Marshal(protocol)
	if strings.Contains(string(encoded), server.URL) || strings.Contains(string(encoded), "synthetic-key") {
		t.Fatalf("poll projection leaked host configuration: %s", encoded)
	}
	initial, err := bridge.ExecutePoll(context.Background(), ExternalPollRequest{Phase: "start", Tool: "short__create", Fingerprint: protocol.Tool.Fingerprint, Arguments: json.RawMessage(`{"prompt":"x"}`)})
	if err != nil || create != 1 {
		t.Fatalf("start %#v %v", initial, err)
	}
	if _, err = bridge.ExecutePoll(context.Background(), ExternalPollRequest{Phase: "query", Tool: "short__create", Fingerprint: protocol.Tool.Fingerprint, Arguments: json.RawMessage(`{"prompt":"x"}`), Initial: initial}); err != nil || query != 1 {
		t.Fatalf("query %v, count=%d", err, query)
	}
	if _, err = bridge.ExecutePoll(context.Background(), ExternalPollRequest{Phase: "query", Tool: "short__create", Fingerprint: "bad", Arguments: json.RawMessage(`{}`), Initial: initial}); err == nil {
		t.Fatal("changed fingerprint accepted")
	}
}

func TestExternalBridgePollWithoutJobDoesNotForwardCrossOriginCredentials(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.URL.Path != "/tasks/frozen" {
			t.Fatal("cross-origin non-Job poll received creation credential")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"done"}`))
	}))
	defer target.Close()
	manifest := Manifest{ID: "short", Name: "short", Version: "1", Tools: []APITool{{
		Name: "create", Description: "x", InputSchema: json.RawMessage(`{"type":"object"}`), URL: "https://example.test/create", Headers: map[string]string{"Authorization": "Bearer {{secret.KEY}}"},
		Poll: &PollSpec{URL: target.URL + "/tasks/{{result.task.id}}", StatusPath: "state", DoneValues: []string{"done"}},
	}}}
	bridge, err := NewExternalBridge([]Installed{{Enabled: true, Manifest: manifest, Secrets: map[string]string{"KEY": "synthetic-key"}}})
	if err != nil {
		t.Fatal(err)
	}
	protocol, _ := bridge.PollProtocol("short__create")
	initial := externalReply([]byte(`{"task":{"id":"frozen"}}`), "application/json")
	if _, err = bridge.ExecutePoll(t.Context(), ExternalPollRequest{Phase: "query", Tool: "short__create", Fingerprint: protocol.Tool.Fingerprint, Arguments: json.RawMessage(`{}`), Initial: initial}); err != nil {
		t.Fatal(err)
	}
}
