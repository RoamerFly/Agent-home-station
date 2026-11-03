package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectCreatePreservesFilesAndChecksScope(t *testing.T) {
	f, p := trustedAgent(t)
	installProjectPluginTestRuntime(t, p)
	ctx := context.Background()
	raw := json.RawMessage(`{"name":"my-tool"}`)
	result, err := p.Call(ctx, "project_create", raw)
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Path string }
	if json.Unmarshal([]byte(result), &out) != nil || out.Path != filepath.Join(p.executionContext().Conversation, "projects", "my-tool") {
		t.Fatal(result)
	}
	file := filepath.Join(out.Path, "keep.txt")
	if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Call(ctx, "project_create", raw); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(file); string(b) != "original" {
		t.Fatal("existing file overwritten")
	}
	for _, name := range []string{"../escape", `a\b`, "con", "COM1", "", "nul"} {
		b, _ := json.Marshal(map[string]string{"name": name})
		if _, err := p.createWorkspaceProject(ctx, b); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	p.trace.Sender = "member"
	if _, err := p.createWorkspaceProject(ctx, json.RawMessage(`{"name":"shared-tool","shared":true}`)); err == nil {
		t.Fatal("nonowner created shared project")
	}
	p.trace.Sender = "wxid_owner"
	shared, err := p.createWorkspaceProject(ctx, json.RawMessage(`{"name":"shared-tool","shared":true}`))
	if err != nil {
		t.Fatal(err)
	}
	var sharedValue map[string]any
	if json.Unmarshal([]byte(shared), &sharedValue) != nil || sharedValue["registered"] != true {
		t.Fatal("shared project did not receive a fixed registration", shared)
	}
	manifest, err := os.ReadFile(filepath.Join(p.executionContext().Public, "manifests", "shared-tool.json"))
	if err != nil || string(manifest) != `{"entry":"projects/shared-tool","id":"shared-tool","name":"shared-tool"}` {
		t.Fatal("unexpected shared registration", string(manifest), err)
	}
	if _, err = p.createWorkspaceProject(ctx, json.RawMessage(`{"name":"shared-tool","shared":true}`)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(p.executionContext().Public, "manifests", "shared-tool.json")); string(b) != string(manifest) {
		t.Fatal("existing shared registration was overwritten")
	}
	c := f.manager.agent.config()
	c.Host.Mode = "read"
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	if _, err := p.createWorkspaceProject(ctx, raw); err == nil {
		t.Fatal("read-only wrote")
	}
}
