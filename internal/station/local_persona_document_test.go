package station

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wechat-bot/internal/companion"
)

func TestSharedPersonaMigrationKeepsWechatSourceAndOwnerEdit(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	old := filepath.Join(data, "applications", "wechat", "legacy-admin", "managed-workspaces", "PERSONA.md")
	if err := os.MkdirAll(filepath.Dir(old), 0700); err != nil {
		t.Fatal(err)
	}
	wechat := "# 微信当前角色\n\n保留原有相处方式。\n"
	if err := os.WriteFile(old, []byte(wechat), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureSharedPersonaDocument(data); err != nil {
		t.Fatal(err)
	}
	path := sharedPersonaDocumentPath(data)
	if got, _, err := readSharedPersonaDocument(path); err != nil || got != wechat {
		t.Fatalf("WeChat Markdown migration: %q, %v", got, err)
	}
	if raw, err := os.ReadFile(old); err != nil || string(raw) != wechat {
		t.Fatalf("source was modified: %q, %v", raw, err)
	}
	ownerEdit := "# 主人修改后的主角色\n\n所有应用共用。\n"
	if err := os.WriteFile(path, []byte(ownerEdit), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureSharedPersonaDocument(data); err != nil {
		t.Fatal(err)
	}
	if got, _, err := readSharedPersonaDocument(path); err != nil || got != ownerEdit {
		t.Fatalf("existing owner edit replaced: %q, %v", got, err)
	}
}

func TestSharedPersonaRejectsInvalidTargetWithoutReplacingIt(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	path := sharedPersonaDocumentPath(data)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureSharedPersonaDocument(data); err == nil {
		t.Fatal("invalid owner document was silently replaced")
	}
	if raw, err := os.ReadFile(path); err != nil || len(raw) != 0 {
		t.Fatalf("invalid owner document changed: %q, %v", raw, err)
	}
}

func TestSharedPersonaHTTPAndApplicationOverlay(t *testing.T) {
	base := t.TempDir()
	f := newLocalHomeFixture(t, filepath.Join(base, "data"), filepath.Join(base, "workspace"))
	path := "/api/persona-document"
	if code, _ := f.request("GET", path, nil, false); code != 401 {
		t.Fatalf("unauthenticated read: %d", code)
	}
	f.login()
	code, before := f.request("GET", path, nil, false)
	if code != 200 || before["path"] != sharedPersonaDocumentPath(f.dir) {
		t.Fatalf("main document view: %d %#v", code, before)
	}
	revision, _ := before["revision"].(string)
	content := "# 大肥鱼\n\n所有应用都友善回应。\n"
	input := map[string]string{"content": content, "revision": revision}
	if code, _ := f.request("POST", path, input, false); code != 403 {
		t.Fatalf("CSRF bypass: %d", code)
	}
	code, saved := f.request("POST", path, input, true)
	if code != 200 || saved["content"] != content || saved["revision"] == revision {
		t.Fatalf("main document save: %d %#v", code, saved)
	}
	if code, _ := f.request("POST", path, input, true); code != 409 {
		t.Fatalf("stale document save: %d", code)
	}
	model, err := f.handler.chat.models.View()
	if err != nil {
		t.Fatal(err)
	}
	model.Persona = "旧表单企图覆盖主角色"
	if _, err := f.handler.chat.models.Save(LocalModelInput{LocalModelSettings: model}); err == nil {
		t.Fatal("model form changed the shared role")
	}
	model.Persona = ""
	if _, err := f.handler.chat.models.Save(LocalModelInput{LocalModelSettings: model, APIKey: "synthetic-persona-key-not-real"}); err != nil {
		t.Fatal("model form without persona was rejected", err)
	}
	if _, snapshot, err := f.handler.chat.models.ClientSnapshot(); err != nil || !strings.Contains(snapshot.Persona, content) {
		t.Fatalf("model did not inherit shared document: %q, %v", snapshot.Persona, err)
	}
	if _, err := (localAppPersonaStore{dir: f.dir}).Save("wechat", localAppPersona{Persona: "只在微信简短说话"}); err != nil {
		t.Fatal(err)
	}
	_, policy, err := (localApplicationModel{store: f.handler.chat.models, app: "wechat"}).prepare("应用协议")
	if err != nil || !strings.Contains(policy, content) || !strings.Contains(policy, "只在微信简短说话") || !strings.Contains(policy, "与主角色冲突时以主角色为准") {
		t.Fatalf("app overlay did not supplement shared role: %q, %v", policy, err)
	}
	_, other, err := (localApplicationModel{store: f.handler.chat.models, app: "douyin"}).prepare("应用协议")
	if err != nil || !strings.Contains(other, content) || strings.Contains(other, "只在微信简短说话") {
		t.Fatalf("app overlay leaked into another app: %q, %v", other, err)
	}
}

func TestLocalConversationAcceptsSharedMarkdownLimit(t *testing.T) {
	if err := companion.Validate(localConversationConfig(strings.Repeat("a", 9000))); err != nil {
		t.Fatalf("local conversation still uses the old inline persona limit: %v", err)
	}
}
