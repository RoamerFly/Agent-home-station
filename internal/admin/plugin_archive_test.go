package admin

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"wechat-bot/internal/pluginhost"
)

func TestPluginArchiveImportRequiresApprovalAndAuth(t *testing.T) {
	f := setup(t)
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	source := "../../plugins/web-search"
	err := filepath.WalkDir(source, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(source, p)
		w, err := z.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	send := func(csrf bool, kind string, body []byte) (int, []byte) {
		t.Helper()
		r, err := http.NewRequest("POST", f.server.URL+"/api/plugin-packages/import", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", kind)
		r.Header.Set("Origin", f.server.URL)
		if csrf {
			r.Header.Set("X-CSRF-Token", f.csrf)
		}
		resp, err := f.client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, b
	}
	if code, _ := send(false, "application/zip", archive.Bytes()); code != 401 {
		t.Fatal("no login", code)
	}
	f.login()
	if code, _ := send(false, "application/zip", archive.Bytes()); code != 403 {
		t.Fatal("no CSRF", code)
	}
	if code, _ := send(true, "application/json", archive.Bytes()); code != 415 {
		t.Fatal("wrong type", code)
	}
	if code, _ := send(true, "application/zip", []byte("broken")); code != 400 {
		t.Fatal("broken ZIP", code)
	}
	code, body := send(true, "application/zip", archive.Bytes())
	if code != 200 {
		t.Fatalf("import %d %s", code, body)
	}
	var p pluginhost.Prepared
	if err = json.Unmarshal(body, &p); err != nil || p.Package.ID != "web-search" {
		t.Fatal("unexpected preparation", err)
	}
	s, err := f.manager.packageStore()
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.List(t.Context())
	if err != nil || len(items) != 0 {
		t.Fatal("import auto installed", err)
	}
	if code, body, _ = f.request("POST", "/api/plugin-packages/install", map[string]any{"token": p.Token, "digest": p.Digest, "confirm": true, "permissions": []string{"network"}}, true, true); code != 201 {
		t.Fatalf("confirm %d %s", code, body)
	}
	i, err := s.Installed(t.Context(), "web-search")
	if err != nil || i.Enabled {
		t.Fatal("installed plugin activated", err)
	}
}
