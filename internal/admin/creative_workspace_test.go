package admin

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/companion"
)

func TestGroupCreationUsesFishWorkspaceWithoutOwnerIdentity(t *testing.T) {
	f, p := trustedAgent(t)
	installProjectPluginFixture(t, p)
	f.manager.mu.Lock()
	f.manager.config.GroupRules = append(f.manager.config.GroupRules, GroupRule{ID: "creative@chatroom"})
	f.manager.mu.Unlock()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "creative@chatroom", Sender: "member", TraceID: "creative-task", TaskID: "creative-task", IsGroup: true})
	if p.privileged() || !p.workspaceExecutionAllowed() || !p.dynamicPluginAllowed() {
		t.Fatal("group creation unexpectedly depends on owner identity")
	}
	names := map[string]bool{}
	for _, tool := range p.workspaceAgentTools() {
		names[tool.Name] = true
	}
	for _, name := range []string{"project_create", "project_checkout_plugin", "station_tool_enable", "workspace_write", "station_comfy"} {
		if !names[name] {
			t.Fatal("creative tool unavailable in group", name)
		}
	}
	result, err := p.mediateWorkspaceDirectory(t.Context(), json.RawMessage(`{"name":"group-art","shared":true}`))
	if err != nil {
		t.Fatal(err)
	}
	path := result.(map[string]any)["path"].(string)
	if path != filepath.Join(p.executionContext().Public, "projects", "group-art") {
		t.Fatal("shared project escaped fish workspace", path)
	}
	config, err := p.hostConfig()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, root := range config.Roots {
		found = found || root == filepath.Join(p.executionContext().Public, "projects")
	}
	if !found {
		t.Fatal("group project cannot be edited in the fish workspace")
	}
	write, _ := json.Marshal(map[string]string{"path": filepath.Join(path, "draft.md"), "content": "synthetic creative draft"})
	if _, err := (agenthost.Host{Config: config}).Call(t.Context(), "server_write", write); err != nil {
		t.Fatal("group project write failed", err)
	}
	copy, err := p.checkoutPluginSource(t.Context(), json.RawMessage(`{"plugin_id":"project-development"}`))
	if err != nil {
		t.Fatal("group plugin source checkout failed", err)
	}
	var checked struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(copy), &checked) != nil || filepath.Dir(filepath.Dir(checked.Path)) != p.executionContext().Conversation {
		t.Fatal("plugin source copy escaped conversation workspace", copy)
	}
}
