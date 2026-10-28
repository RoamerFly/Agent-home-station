package admin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"wechat-bot/internal/bailian"
)

func TestLocalASRDoesNotFallBackToCloudUnlessConfigured(t *testing.T) {
	f := setup(t)
	f.manager.speech.ASREnabled = true
	f.manager.speech.ASRBackend = "local"
	if _, e := f.manager.transcribeAudio(context.Background(), []byte("synthetic")); e == nil || strings.Contains(e.Error(), "密钥") {
		t.Fatal("local selection called cloud", e)
	}
	f.manager.speech.ASRBackend = "local_then_bailian"
	if _, e := f.manager.transcribeAudio(context.Background(), []byte("synthetic")); e == nil || !strings.Contains(e.Error(), "密钥") {
		t.Fatal("explicit fallback not used", e)
	}
	f.manager.speech.ASREnabled = false
	if _, e := f.manager.transcribeAudio(context.Background(), nil); e == nil || !strings.Contains(e.Error(), "未启用") {
		t.Fatal(e)
	}
}

func TestLocalASRManifestRejectsExternalResourcesAndPathTraversal(t *testing.T) {
	f := setup(t)
	public := filepath.Join(f.manager.managedRoot(), "public_workspace")
	os.MkdirAll(filepath.Join(public, "manifests"), 0700)
	b, _ := json.Marshal(localASRManifest{Python: filepath.Join(t.TempDir(), "python")})
	os.WriteFile(filepath.Join(public, "manifests", "asr-sensevoice-small-int8.json"), b, 0600)
	if _, e := f.manager.transcribeLocal(context.Background(), nil, ""); e == nil || !strings.Contains(e.Error(), "outside_public") {
		t.Fatal(e)
	}
	if _, e := f.manager.transcribeLocal(context.Background(), nil, "../external"); e == nil {
		t.Fatal("model traversal")
	}
	if f.manager.localASRStatus()["available"] != false {
		t.Fatal("external manifest reported available")
	}
}

func TestLocalASRUsesIsolatedAccountScratchAndNoWordsDoesNotUpload(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("local isolated Python integration on Linux")
	}
	if _, e := exec.LookPath("bwrap"); e != nil {
		t.Skip("bwrap not installed")
	}
	if _, e := os.Stat("/usr/bin/python3"); e != nil {
		t.Skip("system Python missing")
	}
	f := setup(t)
	public := filepath.Join(f.manager.managedRoot(), "public_workspace")
	for _, name := range []string{"models", "runtimes", "projects", "manifests"} {
		os.MkdirAll(filepath.Join(public, name), 0700)
	}
	python := filepath.Join(public, "runtimes", "python")
	if e := os.Symlink("/usr/bin/python3", python); e != nil {
		t.Fatal(e)
	}
	entry := filepath.Join(public, "projects", "test_asr.py")
	code := "import sys,json,pathlib; p=pathlib.Path(sys.argv[sys.argv.index('--input')+1]); assert p.read_bytes()==b'synthetic-wav'; assert not pathlib.Path('/etc/passwd').exists(); print(json.dumps({'text':'','state':'no_recognizable_speech','range_complete':True}))"
	os.WriteFile(entry, []byte(code), 0600)
	model := filepath.Join(public, "models", "synthetic.onnx")
	os.WriteFile(model, nil, 0600)
	manifest := localASRManifest{Python: python, Entry: entry, Model: model, Tokens: model, Packages: filepath.Join(public, "runtimes")}
	b, _ := json.Marshal(manifest)
	os.WriteFile(filepath.Join(public, "manifests", "asr-sensevoice-small-int8.json"), b, 0600)
	f.manager.speech.ASREnabled = true
	f.manager.speech.ASRBackend = "local_then_bailian"
	_, e := f.manager.transcribeAudio(context.Background(), []byte("synthetic-wav"))
	if !bailian.IsNoSpeech(e) {
		t.Fatal("no-words should not call keyless cloud", e)
	}
	scratch := filepath.Join(f.manager.managedRoot(), "pri_workspace", accountDirectory(f.manager.native.Account()), "scratch")
	entries, e := os.ReadDir(scratch)
	if e != nil || len(entries) != 0 {
		t.Fatal("temporary audio not cleaned", e, entries)
	}
}

func TestLiveLocalASRFromRegisteredPublicModel(t *testing.T) {
	root, input := os.Getenv("WECHAT_LIVE_ASR_WORKSPACE"), os.Getenv("WECHAT_LIVE_ASR_WAV")
	if root == "" || input == "" {
		t.Skip("explicit local-model and audio paths required; no network or WeChat sends")
	}
	t.Setenv("WECHAT_WORKSPACE_ROOT", root)
	f := setup(t)
	f.manager.speech.ASREnabled = true
	f.manager.speech.ASRBackend = "local"
	wav, e := os.ReadFile(input)
	if e != nil {
		t.Fatal(e)
	}
	text, e := f.manager.transcribeAudio(context.Background(), wav)
	if os.Getenv("WECHAT_LIVE_ASR_EXPECT_NO_WORDS") == "1" {
		if !bailian.IsNoSpeech(e) {
			t.Fatal("expected processed no-words outcome", e)
		}
		t.Log("real offline model processed audio with no recognized speech; WeChat sends=0")
		return
	}
	if e != nil {
		t.Fatal(e)
	}
	if strings.TrimSpace(text) == "" {
		t.Fatal("empty known-speech transcription")
	}
	t.Logf("real offline model processed audio; text_characters=%d; WeChat sends=0", len([]rune(text)))
}
