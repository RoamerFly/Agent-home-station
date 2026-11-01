package plugins

import (
	"encoding/json"
	"testing"
)

// Legacy manifests remain validated here.  Their create/poll/cancel HTTP
// phases are exercised through ExternalBridge rather than a duplicate Runtime
// orchestration path.
func backgroundManifest(base string) Manifest {
	return Manifest{ID: "creative", Name: "Synthetic plugin", Tools: []APITool{{Name: "create", Description: "Create test job", InputSchema: json.RawMessage(`{"type":"object"}`), URL: base + "/jobs", Method: "POST", Headers: map[string]string{"Authorization": "Bearer {{secret.KEY}}"}, BodyTemplate: json.RawMessage(`{"prompt":"{{arg.prompt}}"}`), TextPath: "text", Poll: &PollSpec{URL: base + "/jobs/{{result.task.id}}", StatusPath: "state", DoneValues: []string{"done"}, FailedValues: []string{"failed"}}, Job: &JobSpec{IDPath: "task.id", Cancel: &CancelSpec{URL: base + "/jobs/{{result.task.id}}/cancel", BodyTemplate: json.RawMessage(`{"id":"{{result.task.id}}"}`)}}}}}
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
