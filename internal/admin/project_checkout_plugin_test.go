package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wechat-bot/internal/companion"
)

func TestProjectCheckoutPluginCreatesEditablePinnedConversationCopy(t *testing.T) {
	f, p := trustedAgent(t)
	store := installProjectPluginFixture(t, p)
	found := false
	for _, tool := range p.agentTools() {
		found = found || tool.Name == "project_checkout_plugin"
	}
	if !found {
		t.Fatal("owner with project write grant cannot discover checkout tool")
	}
	result, err := p.Call(t.Context(), "project_checkout_plugin", json.RawMessage(`{"plugin_id":"project-development"}`))
	if err != nil {
		t.Fatal(err)
	}
	var first struct {
		State    string `json:"state"`
		Path     string `json:"path"`
		PluginID string `json:"plugin_id"`
		Digest   string `json:"digest"`
		Version  string `json:"version"`
		Files    int    `json:"files"`
	}
	if json.Unmarshal([]byte(result), &first) != nil || first.State != "created" || first.PluginID != projectPluginID || first.Files < 2 {
		t.Fatal("invalid initial checkout", result)
	}
	if want := filepath.Join(p.executionContext().Conversation, "tool-sources") + string(os.PathSeparator); !strings.HasPrefix(first.Path, want) {
		t.Fatal("checkout escaped conversation-private root", first.Path)
	}
	installed, err := store.Installed(t.Context(), projectPluginID)
	if err != nil || first.Digest != installed.Digest || filepath.Base(first.Path) != projectPluginID+"-"+installed.Digest {
		t.Fatal("working copy did not pin the installed digest", first, installed, err)
	}
	if _, err = os.Stat(filepath.Join(first.Path, "plugin.json")); err != nil {
		t.Fatal("manifest missing", err)
	}
	if _, err = os.Stat(filepath.Join(first.Path, "runtime", "main.py")); err != nil {
		t.Fatal("declared source missing", err)
	}
	if _, err = os.Stat(filepath.Join(first.Path, "plugins.sqlite")); !os.IsNotExist(err) {
		t.Fatal("store/configuration material leaked into checkout", err)
	}
	readme := filepath.Join(first.Path, "README.md")
	if err = os.WriteFile(readme, []byte("owner local edit"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = p.Call(t.Context(), "project_checkout_plugin", json.RawMessage(`{"plugin_id":"project-development"}`))
	if err != nil || !strings.Contains(result, `"state":"existing"`) {
		t.Fatal("same digest did not reopen", result, err)
	}
	if data, err := os.ReadFile(readme); err != nil || string(data) != "owner local edit" {
		t.Fatal("idempotent reopen overwrote owner edit", string(data), err)
	}
	// Reconstruct the normal per-turn access object: reopening must come from
	// durable installed-package evidence, not a retained source pointer.
	next, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	next.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "wxid_owner", Sender: "wxid_owner", TraceID: "checkout-reopen", Mode: "direct"})
	result, err = next.Call(context.Background(), "project_checkout_plugin", json.RawMessage(`{"plugin_id":"project-development"}`))
	if err != nil || !strings.Contains(result, `"state":"existing"`) {
		t.Fatal("reopened access lost pinned copy", result, err)
	}
	if data, err := os.ReadFile(readme); err != nil || string(data) != "owner local edit" {
		t.Fatal("reopened access overwrote owner edit", string(data), err)
	}
	for _, raw := range []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{"plugin_id":"../escape"}`), json.RawMessage(`{"plugin_id":"missing-plugin"}`)} {
		if _, err := p.Call(t.Context(), "project_checkout_plugin", raw); err == nil {
			t.Fatal("invalid or missing package checkout accepted", string(raw))
		}
	}
	p.trace.Sender = "member"
	if _, err = p.Call(t.Context(), "project_checkout_plugin", json.RawMessage(`{"plugin_id":"project-development"}`)); err == nil {
		t.Fatal("nonowner checkout accepted")
	}
	p.trace.Sender = "wxid_owner"
	config := f.manager.agent.config()
	config.Host.Mode = "read"
	if err = f.manager.agent.save(config); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Call(t.Context(), "project_checkout_plugin", json.RawMessage(`{"plugin_id":"project-development"}`)); err == nil {
		t.Fatal("read-only project scope checkout accepted")
	}
}

func TestProjectCheckoutPluginRejectsLinkedDestination(t *testing.T) {
	_, p := trustedAgent(t)
	store := installProjectPluginFixture(t, p)
	installed, err := store.Installed(t.Context(), projectPluginID)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(p.executionContext().Conversation, "tool-sources")
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	name, err := checkoutPluginDirectory(projectPluginID, installed.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(t.TempDir(), filepath.Join(root, name)); err != nil {
		t.Skip("symlink creation unavailable", err)
	}
	if _, err = p.Call(t.Context(), "project_checkout_plugin", json.RawMessage(`{"plugin_id":"project-development"}`)); err == nil {
		t.Fatal("linked checkout destination was accepted")
	}
}
