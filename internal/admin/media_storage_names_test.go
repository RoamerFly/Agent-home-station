package admin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"wechat-bot/internal/companion"
)

func TestReadableAssetReuseAndPermanentDeletion(t *testing.T) {
	f := setup(t)
	m := f.manager
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "synthetic"}
	a, err := m.rememberAssetFormats(in, assetOriginal{Data: []byte("synthetic song bytes"), MIME: "audio/wav", Filename: "夜曲-大肥鱼翻唱.wav"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := m.assetPath(a.Hash)
	if !strings.HasSuffix(p, ".wav") || !strings.Contains(p, "夜曲-大肥鱼翻唱") {
		t.Fatal(p)
	}
	if _, err = os.Stat(filepath.Join(m.assetDirectory(), a.Hash)); !os.IsNotExist(err) {
		t.Fatal("hash duplicate remains")
	}
	file, err := m.openVerifiedAsset(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	// A repeated receive must resolve the same readable file, without duplicates.
	if _, err = m.rememberAssetFormats(in, assetOriginal{Data: []byte("synthetic song bytes"), MIME: "audio/wav", Filename: "夜曲-大肥鱼翻唱.wav"}, nil); err != nil {
		t.Fatal(err)
	}
	jobsDir := filepath.Join(m.privateWorkspaceFor("test"), "music", "jobs")
	if err = os.MkdirAll(jobsDir, 0700); err != nil {
		t.Fatal(err)
	}
	jobID := strings.Repeat("a", 32)
	os.WriteFile(filepath.Join(jobsDir, "翻唱.wav"), []byte("synthetic song bytes"), 0600)
	os.WriteFile(filepath.Join(jobsDir, jobID+"-rvc.voice.json"), []byte(`{"profile":"synthetic"}`), 0600)
	os.WriteFile(filepath.Join(jobsDir, "原曲.wav"), []byte("separate original"), 0600)
	ledger, _ := json.Marshal(map[string]any{jobID: map[string]any{"id": jobID, "state": "completed", "file": "翻唱.wav"}})
	os.WriteFile(filepath.Join(jobsDir, "jobs.json"), ledger, 0600)
	preview, err := m.deleteMediaResources(t.Context(), []string{a.Hash}, true)
	if err != nil || len(preview.Files) < 1 {
		t.Fatal(preview, err)
	}
	if _, err = os.Stat(p); err != nil {
		t.Fatal("preview removed file", err)
	}
	result, err := m.deleteMediaResources(t.Context(), []string{a.Hash}, false)
	if err != nil || len(result.Failures) > 0 {
		t.Fatal(result, err)
	}
	if _, err = os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("file remains")
	}
	items, err := m.loadAssets()
	if err != nil || len(items) != 0 {
		t.Fatal(items, err)
	}
	if _, err = m.openAsset(a.Hash); err == nil {
		t.Fatal("deleted resource still readable")
	}
	if _, err = os.Stat(filepath.Join(jobsDir, "翻唱.wav")); !os.IsNotExist(err) {
		t.Fatal("duplicate output remains")
	}
	if _, err = os.Stat(filepath.Join(jobsDir, jobID+"-rvc.voice.json")); !os.IsNotExist(err) {
		t.Fatal("sidecar remains")
	}
	if _, err = os.Stat(filepath.Join(jobsDir, "原曲.wav")); err != nil {
		t.Fatal("original removed", err)
	}
	data, _ := os.ReadFile(filepath.Join(jobsDir, "jobs.json"))
	var jobs map[string]map[string]any
	json.Unmarshal(data, &jobs)
	if jobs[jobID]["file"] != nil || jobs[jobID]["output_deleted"] != true {
		t.Fatal("stale task output reference", string(data))
	}
}

func TestReadableAssetNamesCannotEscapeOrCollide(t *testing.T) {
	a := readableAssetName("../bad:name.wav", strings.Repeat("a", 64))
	b := readableAssetName("../bad:name.wav", strings.Repeat("b", 64))
	if filepath.Base(a) != a || strings.ContainsAny(a, `/\:`) || a == b || filepath.Ext(a) != ".wav" {
		t.Fatal(a, b)
	}
}
