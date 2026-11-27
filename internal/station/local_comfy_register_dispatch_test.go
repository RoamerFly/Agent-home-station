package station

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"wechat-bot/internal/mcptools"
)

func TestLocalComfyRegisterIsCallableOnlyWithOwnerMediaGrant(t *testing.T) {
	f := localChatFixture(t)
	identity, err := f.handler.home.identity.Read()
	if err != nil {
		t.Fatal(err)
	}
	installedRoot := t.TempDir()
	writeLocalComfyChatWorkflow(t, installedRoot)
	backend := &syntheticLocalComfy{root: installedRoot, fish: identity.FishID}
	setUpLocalComfyChat(t, f, backend)
	_, home := f.request("GET", "/api/local/home", nil, false)
	personal := filepath.Join(home["directory"].(string), "workflows", "comfyui")
	if err := os.MkdirAll(personal, 0700); err != nil {
		t.Fatal(err)
	}
	graph := []byte(`{"1":{"class_type":"EmptyImage","inputs":{"width":32,"height":24,"batch_size":1,"color":255}},"9":{"class_type":"SaveImage","inputs":{"images":["1",0],"filename_prefix":"owner-prefix"}}}`)
	if err := os.WriteFile(filepath.Join(personal, "owner-smoke.json"), graph, 0600); err != nil {
		t.Fatal(err)
	}
	tools := noticeToolSet(t, f, "comfy-register-owner", true)
	listed, err := tools.List(t.Context())
	if err != nil || !slices.ContainsFunc(listed, func(item mcptools.Tool) bool { return item.Name == localComfyRegisterTool }) {
		t.Fatalf("owner media tool discovery omitted workflow registration: %v", err)
	}
	arguments := func(id string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{
			"id": id, "label": "Owner smoke", "graph": "owner-smoke.json", "save_node": "9", "parameters": []any{},
		})
		return raw
	}
	result, err := tools.Call(t.Context(), localComfyRegisterTool, arguments("owner-smoke"))
	if err != nil {
		t.Fatal(err)
	}
	var registered struct {
		Registered struct {
			ID string `json:"id"`
		} `json:"registered"`
		WorkflowCount int `json:"workflow_count"`
	}
	if err := json.Unmarshal([]byte(result), &registered); err != nil || registered.Registered.ID != "owner-smoke" || registered.WorkflowCount < 1 {
		t.Fatalf("register call did not reach the Comfy handler: %s %v", result, err)
	}
	if backend.starts != 0 || backend.submits != 0 || backend.running {
		t.Fatal("workflow registration started the synthetic Comfy service")
	}
	startedResult, err := tools.Call(t.Context(), localComfyStartTool,
		json.RawMessage(`{"workflow_id":"smoke","inputs":{"width":48}}`))
	var started struct {
		JobID string `json:"job_id"`
	}
	if err != nil || json.Unmarshal([]byte(startedResult), &started) != nil || started.JobID == "" || backend.submits != 1 {
		t.Fatalf("synthetic original job was not submitted: %s %v", startedResult, err)
	}
	before, err := os.ReadFile(filepath.Join(personal, localComfyWorkflowRegistryFile))
	if err != nil {
		t.Fatal(err)
	}
	tools.grants.Media = false
	listed, err = tools.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{localComfyListTool, localComfyModelsTool, localComfyFilesTool, localComfyRegisterTool, localComfyStartTool} {
		if slices.ContainsFunc(listed, func(item mcptools.Tool) bool { return item.Name == name }) {
			t.Fatalf("withdrawn media grant still exposed new ComfyUI action %q", name)
		}
	}
	if !slices.ContainsFunc(listed, func(item mcptools.Tool) bool { return item.Name == localComfyJobTool }) {
		t.Fatal("original ComfyUI job status/cancel vanished from the Agent catalog")
	}
	if value, err := tools.Call(t.Context(), localComfyRegisterTool, arguments("owner-second")); value != "" || err == nil || !strings.Contains(err.Error(), "未授权本机媒体") {
		t.Fatalf("hidden workflow registration bypassed media grant: %s %v", value, err)
	}
	status, _ := json.Marshal(map[string]string{"job_id": started.JobID, "action": "status"})
	if value, err := tools.Call(t.Context(), localComfyJobTool, status); err != nil || !strings.Contains(value, `"state":"running"`) {
		t.Fatalf("media withdrawal blocked original job status: %s %v", value, err)
	}
	cancel, _ := json.Marshal(map[string]string{"job_id": started.JobID, "action": "cancel"})
	if value, err := tools.Call(t.Context(), localComfyJobTool, cancel); err != nil || !strings.Contains(value, `"state":"cancelled"`) || backend.cancels != 1 || backend.submits != 1 {
		t.Fatalf("media withdrawal blocked original job cancellation: %s %v", value, err)
	}
	tools.grants.Media = true
	tools.publicUseOnly = true
	if value, err := tools.Call(t.Context(), localComfyRegisterTool, arguments("owner-second")); value != "" || err == nil || !strings.Contains(err.Error(), "未授权本机媒体") {
		t.Fatalf("application-scoped tool set reached owner workflow registration: %s %v", value, err)
	}
	if value, err := tools.Call(t.Context(), localComfyJobTool, status); value != "" || err == nil {
		t.Fatalf("application-scoped tool set reached owner task status: %s %v", value, err)
	}
	if after, err := os.ReadFile(filepath.Join(personal, localComfyWorkflowRegistryFile)); err != nil || string(after) != string(before) {
		t.Fatal("denied call changed registered workflow", err)
	}
}
