package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVoiceLibraryRequiresLoginAndServesOnlyRootedObjects(t *testing.T) {
	f := setup(t)
	if code, _, _ := f.request("GET", "/api/voice-library", nil, false, false); code != 401 {
		t.Fatal(code)
	}
	if code, _, _ := f.request("GET", "/voice-library", nil, false, false); code != 401 {
		t.Fatal(code)
	}
	f.login()
	for path, want := range map[string]string{"/assets/voices.js": "text/javascript", "/assets/voices.css": "text/css"} {
		code, _, response := f.request("GET", path, nil, false, false)
		if code != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), want) {
			t.Fatal("static voice MIME mismatch", path, code, response.Header.Get("Content-Type"))
		}
	}

	if code, b, _ := f.request("GET", "/api/voice-library", nil, false, false); code != 200 || !bytes.Contains(b, []byte(`"items":[]`)) {
		t.Fatal(code, string(b))
	}
	dir := filepath.Join(f.manager.dir, "voice-library")
	if err := os.MkdirAll(filepath.Join(dir, "files"), 0700); err != nil {
		t.Fatal(err)
	}
	name := strings.Repeat("a", 64) + ".wav"
	if err := writePrivate(filepath.Join(dir, "files", name), []byte("synthetic audio")); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(filepath.Join(dir, "catalog.json"), []byte(`{"version":1,"items":[{"name":"synthetic","engine":"GPT-SoVITS"}]}`)); err != nil {
		t.Fatal(err)
	}
	if code, b, _ := f.request("GET", "/api/voice-library", nil, false, false); code != 200 || !json.Valid(b) {
		t.Fatal(code, string(b))
	}
	if code, b, _ := f.request("GET", "/api/voice-library/files/"+name, nil, false, false); code != 200 || string(b) != "synthetic audio" {
		t.Fatal(code)
	}
	for _, bad := range []string{"admin-password", "catalog.json", strings.Repeat("a", 64) + ".exe", "%2e%2e%2fadmin-password"} {
		code, _, _ := f.request(http.MethodGet, "/api/voice-library/files/"+bad, nil, false, false)
		if code == 200 {
			t.Fatal("non-object escaped", bad)
		}
	}
	if code, b, _ := f.request("GET", "/voice-library", nil, false, false); code != 200 || !bytes.Contains(b, []byte("角色语音库")) {
		t.Fatal(code)
	}
}
