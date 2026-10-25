package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func backgroundManifest(base string) Manifest {
	return Manifest{ID: "creative", Name: "Synthetic plugin", Tools: []APITool{{Name: "create", Description: "Create test job", InputSchema: json.RawMessage(`{"type":"object"}`), URL: base + "/jobs", Method: "POST", Headers: map[string]string{"Authorization": "Bearer {{secret.KEY}}"}, BodyTemplate: json.RawMessage(`{"prompt":"{{arg.prompt}}"}`), TextPath: "text", Poll: &PollSpec{URL: base + "/jobs/{{result.task.id}}", StatusPath: "state", DoneValues: []string{"done"}, FailedValues: []string{"failed"}}, Job: &JobSpec{IDPath: "task.id", Cancel: &CancelSpec{URL: base + "/jobs/{{result.task.id}}/cancel", BodyTemplate: json.RawMessage(`{"id":"{{result.task.id}}"}`)}}}}}
}
func TestBackgroundJobCreatesOnceAndResumesByFrozenIdentity(t *testing.T) {
	creates, polls, cancels := 0, 0, 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-plugin-key" {
			t.Error("credentials missing")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /jobs":
			creates++
			w.Write([]byte(`{"task":{"id":"job-1"},"state":"queued"}`))
		case "GET /jobs/job-1":
			polls++
			w.Write([]byte(`{"state":"done","text":"real task output"}`))
		case "POST /jobs/job-1/cancel":
			cancels++
			w.Write([]byte(`{"accepted":true}`))
		default:
			t.Error("unexpected request", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	m := backgroundManifest(s.URL)
	r, err := New([]Installed{{Manifest: m, Enabled: true, Secrets: map[string]string{"KEY": "synthetic-plugin-key"}}})
	if err != nil {
		t.Fatal(err)
	}
	ticket, _ := r.NewJobTicket("creative__create")
	ticket, err = r.StartJob(context.Background(), ticket, json.RawMessage(`{"prompt":"test"}`))
	if err != nil || ticket.RemoteID != "job-1" || ticket.State != "waiting" {
		t.Fatal(ticket, err)
	}
	if _, err = r.StartJob(context.Background(), ticket, json.RawMessage(`{"prompt":"test"}`)); err == nil {
		t.Fatal("ticket resubmitted")
	}
	stored, _ := json.Marshal(ticket)
	var reloaded JobTicket
	json.Unmarshal(stored, &reloaded)
	r, _ = New([]Installed{{Manifest: m, Enabled: true, Secrets: map[string]string{"KEY": "synthetic-plugin-key"}}})
	reloaded, err = r.PollJob(context.Background(), reloaded)
	if err != nil || reloaded.State != "completed" || reloaded.Output.Text != "real task output" || creates != 1 || polls != 1 {
		t.Fatal(reloaded, err, creates, polls)
	}
	if err = r.CancelJob(context.Background(), ticket); err != nil || cancels != 1 {
		t.Fatal(err, cancels)
	}
	m.Tools[0].URL = s.URL + "/new-owner-endpoint"
	changed, _ := New([]Installed{{Manifest: m, Enabled: true, Secrets: map[string]string{"KEY": "synthetic-plugin-key"}}})
	if _, err = changed.PollJob(context.Background(), ticket); err == nil {
		t.Fatal("old job rebound to new manifest")
	}
	if creates != 1 || polls != 1 {
		t.Fatal("read or config change replayed create")
	}
	if strings.Contains(string(stored), "synthetic-plugin-key") {
		t.Fatal("secret persisted in ticket")
	}
}

func TestBackgroundManifestRejectsDynamicHostsAndUnrecoverableTemplates(t *testing.T) {
	for _, mutate := range []func(*APITool){
		func(t *APITool) { t.Poll = nil },
		func(t *APITool) { t.Poll.URL = "https://example.org/jobs/{{result.other}}" },
		func(t *APITool) { t.Poll.Headers = map[string]string{"X-Value": "{{arg.private}}"} },
		func(t *APITool) { t.Job.Cancel.URL = "https://{{result.task.id}}/cancel" },
		func(t *APITool) { t.Job.Cancel.BodyTemplate = json.RawMessage(`{"value":"{{arg.prompt}}"}`) },
		func(t *APITool) { t.Job.MaxPolls = 1441 },
	} {
		m := backgroundManifest("https://example.org")
		mutate(&m.Tools[0])
		if Validate(m) == nil {
			t.Fatal("unsafe background manifest accepted", m)
		}
	}
}

func TestBackgroundPollingDoesNotForwardCreationCredentialsAcrossOrigins(t *testing.T) {
	seen := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = true
		if r.Header.Get("Authorization") != "" {
			t.Error("creation secret crossed origin")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"state":"done","text":"okay"}`))
	}))
	defer target.Close()
	m := backgroundManifest("https://example.org")
	m.Tools[0].Poll.URL = target.URL + "/jobs/{{result.task.id}}"
	r, err := New([]Installed{{Manifest: m, Enabled: true, Secrets: map[string]string{"KEY": "synthetic-plugin-key"}}})
	if err != nil {
		t.Fatal(err)
	}
	ticket, _ := r.NewJobTicket("creative__create")
	ticket.RemoteID = "job-1"
	if _, err = r.PollJob(context.Background(), ticket); err != nil || !seen {
		t.Fatal(err, seen)
	}
}

func TestBackgroundNumericIdentifierDoesNotLosePrecision(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"task":{"id":18446744073709551615},"state":"waiting"}`))
	}))
	defer s.Close()
	r, err := New([]Installed{{Manifest: backgroundManifest(s.URL), Enabled: true, Secrets: map[string]string{"KEY": "synthetic-plugin-key"}}})
	if err != nil {
		t.Fatal(err)
	}
	ticket, _ := r.NewJobTicket("creative__create")
	ticket, err = r.StartJob(context.Background(), ticket, json.RawMessage(`{"prompt":"synthetic"}`))
	if err != nil || ticket.RemoteID != "18446744073709551615" {
		t.Fatal(ticket, err)
	}
}
