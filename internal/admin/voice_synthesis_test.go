package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNamedVoiceIsDiscoverableWithoutBailianKeyAndNeverFallsBack(t *testing.T) {
	f, p, _, _ := voiceAgentFixture(t)
	c := f.manager.agent.config()
	c.Host.Mode = "full"
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	f.manager.speech.Key = ""
	f.manager.speech.TTSEnabled = false
	f.manager.config.Companion.Permissions.SendVoice = true
	tools := p.speechTools()
	if len(tools) != 1 || tools[0].Name != "speech_synthesize" || !strings.Contains(string(tools[0].InputSchema), "voice_id") {
		t.Fatal("local TTS hidden by unrelated cloud key", tools)
	}
	if _, err := p.generateSpeech(context.Background(), "speech_synthesize", json.RawMessage(`{"voice_id":"missing","text":"hello"}`)); err == nil || !strings.Contains(err.Error(), "voice_not_found") {
		t.Fatal("unknown voice fell through to default provider", err)
	}
	p.trace.Sender = "ordinary"
	p.trace.Thread = "ordinary"
	f.manager.config.PrivateEnabled = true
	f.manager.config.DirectIDs = append(f.manager.config.DirectIDs, "ordinary")
	if _, err := p.synthesizeLibraryVoice(context.Background(), json.RawMessage(`{"voice_id":"owner-voice","text":"hello"}`)); err == nil || !strings.Contains(err.Error(), "not_authorized") {
		t.Fatal("ordinary member obtained private voice", err)
	}
}

func TestNamedVoiceExecutesRegisteredEngineInSandboxAndStagesActualWav(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux isolated runtime fixture")
	}
	for _, name := range []string{"bwrap", "ffmpeg", "ffprobe", "python3"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip(name, "unavailable")
		}
	}
	f, p, voice, _ := voiceAgentFixture(t)
	c := f.manager.agent.config()
	c.Host.Mode = "full"
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	f.manager.config.Companion.Permissions.SendVoice = true
	native := &capturedVoiceNative{fakeTools: &fakeTools{fakeNative: f.native}}
	f.manager.native = native
	library := f.manager.privateVoiceLibrary()
	add := func(filename, kind string) voiceFile {
		data := []byte("synthetic " + filename)
		sha := sha256.Sum256(data)
		hash := hex.EncodeToString(sha[:])
		id := hash + filepath.Ext(filename)
		if err := writePrivate(filepath.Join(library, "files", id), data); err != nil {
			t.Fatal(err)
		}
		return voiceFile{File: id, Filename: filename, Kind: kind, Hash: hash, Bytes: int64(len(data))}
	}
	voice.Weights = append(voice.Weights, add("voice.pth", "sovits"))
	voice.Recommended["sovits"] = voice.Weights[1].File
	voice.References = []voiceFile{add("reference.wav", "reference"), add("reference.txt", "reference")}
	data, _ := json.Marshal(map[string]any{"items": []voiceEntry{voice}})
	if err := writePrivate(filepath.Join(library, "catalog.json"), data); err != nil {
		t.Fatal(err)
	}
	public := p.executionContext().Public
	project := filepath.Join(public, "projects", "synthetic-tts")
	if err := ensureManaged(project); err != nil {
		t.Fatal(err)
	}
	python := filepath.Join(project, "python")
	entry := filepath.Join(project, "synthesize.py")
	if err := os.WriteFile(python, []byte("#!/bin/sh\nexec /usr/bin/python3 \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	script := `import json,sys,wave,struct
from pathlib import Path
a=sys.argv[1:]
get=lambda key:a[a.index(key)+1]
assert get('--text')=="hello '; touch must-not-run"
assert json.loads(Path(get('--parameters')).read_text())['sample_steps']==64
assert Path(get('--gpt')).read_bytes().startswith(b'synthetic')
assert not Path('/mnt/c').exists()
try: Path(get('--project'),'must-not-write').write_text('bad')
except OSError: pass
else: raise AssertionError('public project writable')
with wave.open(get('--output'),'wb') as f:
 f.setnchannels(1);f.setsampwidth(2);f.setframerate(16000);f.writeframes(struct.pack('<h',1000)*16000)
print(json.dumps({'complete':True}))
`
	if err := os.WriteFile(entry, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]string{"id": "gpt-sovits", "name": "Synthetic TTS", "root": project, "python": python, "entry": entry, "base_models": filepath.Join(public, "models"), "runtime_status": "synthetic_registered"})
	if err := writePrivate(filepath.Join(public, "manifests", "gpt-sovits.json"), manifest); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"voice_id": voice.ID, "text": "hello '; touch must-not-run", "device": "cpu", "parameters": map[string]int{"sample_steps": 64}, "timeout_seconds": 30})
	result, err := p.synthesizeLibraryVoice(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if json.Unmarshal([]byte(result), &value) != nil || value["voice_id"] != voice.ID || value["synthesized"] != true || len(native.parts) != 1 {
		t.Fatal(result, len(native.parts))
	}
	assets, assetErr := p.manager.loadAssets()
	if assetErr != nil {
		t.Fatal(assetErr)
	}
	namedAsset := false
	for _, a := range assets {
		if a.VoiceID == voice.ID && a.VoiceEngine == voice.Engine {
			namedAsset = true
		}
	}
	if !namedAsset {
		t.Fatal("named voice identity only existed in tool response, not durable asset")
	}
	if !strings.Contains(p.AgentContext(context.Background()), "synthetic_registered") {
		t.Fatal("current registration missing from fresh model context")
	}
	if _, err := os.Stat(filepath.Join(project, "must-not-write")); !os.IsNotExist(err) {
		t.Fatal("sandbox wrote public source")
	}
}
