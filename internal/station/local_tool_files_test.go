package station

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolFileCatalogPreservesMediaAndProvenance(t *testing.T) {
	data := t.TempDir()
	s := &localProjectTools{home: &localHomeService{dataDir: data}}
	base := filepath.Join(data, "applications", "wechat", "legacy-admin")
	paths := []string{"managed-workspaces/account/assets/objects/abc", "managed-workspaces/account/music/jobs/song.wav", "managed-workspaces/account/voice-library/model.wav", "managed-workspaces/account/secrets/auth.json"}
	for _, p := range paths {
		full := filepath.Join(base, p)
		os.MkdirAll(filepath.Dir(full), 0700)
		os.WriteFile(full, []byte("sample"), 0600)
	}
	os.WriteFile(filepath.Join(base, "media-assets.json"), []byte(`[{"hash":"abc","filename":"原曲.mp4","first_request":{"requester_name":"测试主人","request":"请翻唱","task_id":"test-task"}}]`), 0600)
	files, warnings := s.toolFiles(context.Background())
	if len(warnings) > 0 || len(files) != 2 {
		t.Fatal(files, warnings)
	}
	b, _ := json.Marshal(files)
	if !strings.Contains(string(b), "微信请求 · 测试主人 · 请翻唱") || strings.Contains(string(b), "model.wav") {
		t.Fatal(string(b))
	}
	for _, f := range files {
		if _, err := os.Stat(f.Path); err != nil {
			t.Fatal("listing mutated media", err)
		}
	}
}
func TestToolFileKindsExcludeModels(t *testing.T) {
	for _, p := range []string{"/tools/models/a.wav", "/data/catalog/a.wav", "/data/voice-library/a.wav"} {
		if toolFileKind(p) != "" {
			t.Fatal(p)
		}
	}
}
func TestLifecycleCatalogIncludesAllOfficialTools(t *testing.T) {
	s := &localProjectTools{}
	items := s.packages(t.Context(), nil, nil)
	found := map[string]bool{}
	for _, p := range items {
		if found[p.ID] {
			t.Fatal("duplicate", p.ID)
		}
		found[p.ID] = true
	}
	for _, id := range []string{"music", "speech", "share-reader", "web-search", "rvc", "yue", "audio-cpp"} {
		if !found[id] {
			t.Fatal("missing", id)
		}
	}
}

func TestFormalToolFilesHideScratchAndValidation(t *testing.T) {
	for _, p := range []string{"/data/a/null-1.tmp", "/data/music/x-voice-work/vocals.wav", "/data/validation-20261117/mom.wav", "/data/music/input/source.wav"} {
		if toolFileKind(p) != "" {
			t.Fatal("intermediate visible", p)
		}
	}
}
