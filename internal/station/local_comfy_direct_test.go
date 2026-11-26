package station

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/companion"
)

func TestDirectComfyRunAndJobDoNotCallModel(t *testing.T) {
	f := localComfyFixture(t)
	record, err := f.handler.home.identity.Read()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeLocalComfyChatWorkflow(t, root)
	backend := &syntheticLocalComfy{root: root, fish: record.FishID}
	setUpLocalComfyChat(t, f, backend)
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return nil, errors.New("model must not be called by direct ComfyUI tool")
	}
	const route = "/api/local/project-tools/comfyui/action"
	if code, _ := f.request("POST", route, map[string]any{"request_id": "missing-csrf", "action": "workflows"}, false); code != http.StatusForbidden {
		t.Fatalf("direct tool route lacked owner CSRF protection: %d", code)
	}
	request := map[string]any{"request_id": "wechat-comfy-run-1", "action": "run", "workflow_id": "smoke", "inputs": map[string]any{"width": 48}}
	code, result := f.request("POST", route, request, true)
	if code != http.StatusOK || backend.submits != 1 {
		t.Fatalf("direct run: %d %#v, submits=%d", code, result, backend.submits)
	}
	view, ok := result["result"].(map[string]any)
	if !ok || view["state"] == nil || view["job_id"] == nil {
		t.Fatalf("direct run did not return project job: %#v", result)
	}
	jobID, _ := view["job_id"].(string)
	code, repeated := f.request("POST", route, request, true)
	if code != http.StatusOK || repeated["reused"] != true || backend.submits != 1 {
		t.Fatalf("same request repeated a model or job: %d %#v submits=%d", code, repeated, backend.submits)
	}
	request["inputs"] = map[string]any{"width": 49}
	if code, _ := f.request("POST", route, request, true); code != http.StatusConflict || backend.submits != 1 {
		t.Fatalf("changed request reused original source: %d submits=%d", code, backend.submits)
	}
	code, status := f.request("POST", route, map[string]any{"request_id": "wechat-comfy-status-1", "action": "job", "job_id": jobID}, true)
	if code != http.StatusOK || status["result"] == nil {
		t.Fatalf("direct job status: %d %#v", code, status)
	}
	code, cancelled := f.request("POST", route, map[string]any{"request_id": "wechat-comfy-cancel-1", "action": "job", "job_id": jobID, "job_action": "cancel"}, true)
	if code != http.StatusOK || backend.cancels != 1 {
		t.Fatalf("direct job cancel: %d %#v cancels=%d", code, cancelled, backend.cancels)
	}
	code, repeatedCancel := f.request("POST", route, map[string]any{"request_id": "wechat-comfy-cancel-1", "action": "job", "job_id": jobID, "job_action": "cancel"}, true)
	if code != http.StatusOK || repeatedCancel["reused"] != true || backend.cancels != 1 {
		t.Fatalf("direct cancel was not idempotent: %d %#v cancels=%d", code, repeatedCancel, backend.cancels)
	}
	code, catalog := f.request("POST", route, map[string]any{"request_id": "wechat-comfy-workflows-1", "action": "workflows"}, true)
	if code != http.StatusOK || !strings.Contains(fmt.Sprint(catalog["result"]), "Offline smoke") {
		t.Fatalf("direct workflows: %d %#v", code, catalog)
	}
	if backend.submits != 1 {
		t.Fatal("read-only calls re-submitted job")
	}
	store, legacy, err := f.handler.chat.conversation()
	if err != nil {
		t.Fatal(err)
	}
	legacy.Text = "调用本机 project_comfy_run，workflow_id=smoke，inputs={}."
	legacy.OriginalText = &legacy.Text
	legacy.TraceID = "wechat-comfy-legacy-1"
	if err := store.Observe(legacy, localConversationConfig(""), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	code, old := f.request("POST", route, map[string]any{"request_id": legacy.TraceID, "action": "run", "workflow_id": "smoke"}, true)
	if code != http.StatusOK || old["reused"] != true || backend.submits != 1 {
		t.Fatalf("legacy request was resubmitted: %d %#v submits=%d", code, old, backend.submits)
	}
	oldView, _ := old["result"].(map[string]any)
	if oldView["state"] != "legacy_in_progress" {
		t.Fatalf("legacy state=%#v", oldView)
	}
}
