package admin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/agenthost"
	"wechat-bot/internal/companion"
)

func TestMemberDocumentToolsAndConversationIsolation(t *testing.T) {
	f, p, _, _ := artifactPipelineFixture(t)
	f.manager.mu.Lock()
	f.manager.config.DirectIDs = append(f.manager.config.DirectIDs, "alice", "bob")
	f.manager.mu.Unlock()
	set := func(sender string) {
		p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: sender, Sender: sender, TraceID: "task", TaskID: "task"})
	}
	set("alice")
	if p.privileged() {
		t.Fatal("member became owner")
	}
	names := map[string]bool{}
	for _, tool := range p.workspaceAgentTools() {
		names[tool.Name] = true
	}
	for _, name := range []string{"artifact_workspace", "artifact_render_document", "artifact_document_memory", "station_tool_status", "station_tool_start", "station_tool_stop", "station_tool_restart"} {
		if !names[name] {
			t.Fatal("missing member capability", name)
		}
	}
	if names["agent_global_search"] || names["project_checkout_plugin"] {
		t.Fatal("member gained global privilege")
	}
	raw, e := p.artifactWorkspace(t.Context(), json.RawMessage(`{}`))
	if e != nil {
		t.Fatal(e)
	}
	var a map[string]any
	json.Unmarshal([]byte(raw), &a)
	note := filepath.Join(a["learning_dir"].(string), "style", "note.md")
	cfg, e := p.hostConfig()
	if e != nil {
		t.Fatal(e)
	}
	args, _ := json.Marshal(map[string]string{"path": note, "content": "synthetic style"})
	if _, e = (agenthost.Host{Config: cfg}).Call(t.Context(), "server_write", args); e != nil {
		t.Fatal(e)
	}
	set("bob")
	cfg, e = p.hostConfig()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = (agenthost.Host{Config: cfg}).Call(t.Context(), "server_write", args); e == nil {
		t.Fatal("other member modified notes")
	}
	if b, e := os.ReadFile(note); e != nil || string(b) != "synthetic style" {
		t.Fatal("note changed")
	}
	set("alice")
	p.trace.TaskID = "next"
	next, e := p.artifactWorkspace(t.Context(), json.RawMessage(`{}`))
	if e != nil {
		t.Fatal(e)
	}
	var b map[string]any
	json.Unmarshal([]byte(next), &b)
	if a["learning_dir"] != b["learning_dir"] {
		t.Fatal("learning lost across tasks")
	}
}

func TestMembersShareGroupLearningAndAssets(t *testing.T) {
	f, p, native, _ := artifactPipelineFixture(t)
	f.manager.mu.Lock()
	f.manager.config.GroupRules = append(f.manager.config.GroupRules, GroupRule{ID: "shared@chatroom"}, GroupRule{ID: "other@chatroom"})
	f.manager.mu.Unlock()
	set := func(sender, group string) {
		p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: group, Sender: sender, TraceID: "upload", TaskID: sender})
	}
	set("alice", "shared@chatroom")
	asset, err := f.manager.rememberAssetFormats(p.trace, assetOriginal{Data: []byte("group document"), MIME: "text/plain", Filename: "group.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := p.artifactWorkspace(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	set("bob", "shared@chatroom")
	second, err := p.artifactWorkspace(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var a, b map[string]any
	json.Unmarshal([]byte(first), &a)
	json.Unmarshal([]byte(second), &b)
	if a["learning_dir"] != b["learning_dir"] {
		t.Fatal("same group learning not shared")
	}
	got, err := p.MaterializeArtifactForTask(t.Context(), asset.ID, "upload", "")
	if err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(got.InputPath); err != nil || string(content) != "group document" {
		t.Fatal("shared original unavailable", err)
	}
	set("bob", "other@chatroom")
	if _, err = p.MaterializeArtifactForTask(t.Context(), asset.ID, "upload", ""); err == nil {
		t.Fatal("cross-group asset admitted")
	}
	if native.submits.Load() != 0 {
		t.Fatal("read test sent message")
	}
}

func TestMemberMusicStartStatusButNotForceStop(t *testing.T) {
	f, p, _ := musicServiceFixture(t)
	f.manager.config.GroupRules = append(f.manager.config.GroupRules, GroupRule{ID: "member@chatroom"})
	p.trace.Thread = "member@chatroom"
	p.trace.Source = nil
	if err := f.manager.memory.Observe(p.trace, f.manager.config.Companion, time.Now()); err != nil {
		t.Fatal(err)
	}
	p.trace.Source, _ = f.manager.memory.SourceForInput(p.trace)
	config := f.manager.agent.config()
	config.OwnerIDs = nil
	config.Host.Mode = "files"
	if err := f.manager.agent.save(config); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"start", "status"} {
		if _, err := p.newMusicServiceControlPolicy(t.Context(), action); err != nil {
			t.Fatal(action, err)
		}
	}
	if _, err := p.newMusicServiceControlPolicy(t.Context(), "stop"); err == nil {
		t.Fatal("member forced backend stop")
	}
}
