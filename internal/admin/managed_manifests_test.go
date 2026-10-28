package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectCatalogRetainsValidProjectsAndExplainsInvalidMetadata(t *testing.T) {
	_, p := trustedAgent(t)
	dir := filepath.Join(p.executionContext().Public, "manifests")
	files := map[string]string{"valid.json": `{"id":"valid","usage":"synthetic"}`, "bad.json": "{", "null.json": "null", "array.json": "[]", "large.json": `{"data":"` + strings.Repeat("x", projectManifestBytes) + `"}`, "edge.json": `{"id":"edge"}` + strings.Repeat(" ", projectManifestBytes-len(`{"id":"edge"}`))}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := p.Call(context.Background(), "project_list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		Projects []struct{ ID string }
		Issues   []projectManifestIssue `json:"unavailable_manifests"`
	}
	if json.Unmarshal([]byte(result), &view) != nil || len(view.Projects) != 2 || view.Projects[0].ID != "edge" || view.Projects[1].ID != "valid" || len(view.Issues) != 4 {
		t.Fatal(result)
	}
	for _, id := range []string{"bad", "null", "array", "large", "../valid", "..\\valid", "valid:stream"} {
		raw, _ := json.Marshal(map[string]string{"id": id})
		if _, err := p.Call(context.Background(), "project_read", raw); err == nil {
			t.Fatal("invalid manifest accepted", id)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	data, err := readProjectManifest(root, "edge.json")
	if err != nil || len(data) != projectManifestBytes {
		t.Fatal("metadata boundary", len(data), err)
	}
}

func TestProjectManifestLinksCannotExposeOtherRoots(t *testing.T) {
	_, p := trustedAgent(t)
	public := p.executionContext().Public
	dir := filepath.Join(public, "manifests")
	outside := t.TempDir()
	secret := filepath.Join(outside, "private.json")
	if err := os.WriteFile(secret, []byte(`{"synthetic_secret":"outside-root"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "escape.json")); err != nil {
		t.Skip("symlink creation unavailable", err)
	}
	if _, err := p.Call(context.Background(), "project_read", json.RawMessage(`{"id":"escape"}`)); err == nil {
		t.Fatal("linked manifest exposed")
	}
	result, err := p.Call(context.Background(), "project_list", json.RawMessage(`{}`))
	if err != nil || strings.Contains(result, "outside-root") {
		t.Fatal(result, err)
	}
	if err := os.Rename(dir, dir+"-original"); err != nil {
		t.Skip("directory rename unavailable", err)
	}
	if err := os.Symlink(outside, dir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readProjectManifests(context.Background(), public, "private", true); err == nil {
		t.Fatal("linked manifest directory exposed")
	}
}
