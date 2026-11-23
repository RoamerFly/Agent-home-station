package admin

import (
	"encoding/json"
	"os"
	"testing"
)

func TestPersonaDocumentPageEditsCanonicalMarkdown(t *testing.T) {
	f := setup(t)
	if code, _, _ := f.request("GET", "/api/persona-document", nil, false, false); code != 401 {
		t.Fatal("persona document accessible without session")
	}
	f.login()
	code, body, _ := f.request("GET", "/api/persona-document", nil, false, false)
	var current struct {
		Content  string `json:"content"`
		Revision string `json:"revision"`
	}
	if code != 200 || json.Unmarshal(body, &current) != nil || current.Revision == "" {
		t.Fatal("persona document unavailable")
	}
	content := "# 鲸鱼娘\n\n直接回应当前问题。\n"
	code, body, _ = f.request("POST", "/api/persona-document", map[string]string{"content": content, "revision": current.Revision}, true, true)
	if code != 200 {
		t.Fatal(code, string(body))
	}
	stored, err := os.ReadFile(f.manager.personaDocumentPath())
	if err != nil || string(stored) != content || !f.manager.Config().Companion.PersonaDocument || f.manager.Config().Companion.Persona != content {
		t.Fatal("Markdown and active character diverged")
	}
	if code, _, _ := f.request("POST", "/api/persona-document", map[string]string{"content": "# stale", "revision": current.Revision}, true, true); code != 409 {
		t.Fatal("stale editor overwrote newer Markdown")
	}
}
