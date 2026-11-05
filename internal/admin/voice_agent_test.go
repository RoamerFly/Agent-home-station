package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func voiceAgentFixture(t *testing.T) (*webFixture, *permittedTools, voiceEntry, voiceEntry) {
	t.Helper()
	f, p := trustedAgent(t)
	c := f.manager.agent.config()
	c.CrossConversation = true
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	makeEntry := func(id string, public bool) voiceEntry {
		bytes := []byte("synthetic " + id)
		sum := sha256.Sum256(bytes)
		hash := hex.EncodeToString(sum[:])
		name := hash + ".ckpt"
		root := filepath.Join(f.manager.privateVoiceLibrary(), "files")
		if public {
			root = filepath.Join(p.executionContext().Public, "models", "voice-library", "public-example")
		}
		if err := ensureManaged(root); err != nil {
			t.Fatal(err)
		}
		if err := writePrivate(filepath.Join(root, name), bytes); err != nil {
			t.Fatal(err)
		}
		file := voiceFile{File: name, Filename: id + ".ckpt", Hash: hash, Bytes: int64(len(bytes)), Kind: "gpt"}
		if public {
			file.File = ""
			file.Path = filepath.Join(root, name)
		}
		return voiceEntry{ID: id, Name: id, Engine: "GPT-SoVITS", Weights: []voiceFile{file}, References: []voiceFile{}, Previews: []voiceFile{}, Recommended: map[string]string{"gpt": name}}
	}
	private, public := makeEntry("owner-voice", false), makeEntry("public-voice", true)
	public.Recommended = nil // Public imports use path/hash, without private object IDs.
	for dir, v := range map[string]voiceEntry{f.manager.privateVoiceLibrary(): private, filepath.Join(p.executionContext().Public, "models", "voice-library"): public} {
		b, _ := json.Marshal(map[string]any{"items": []voiceEntry{v}})
		if err := writePrivate(filepath.Join(dir, "catalog.json"), b); err != nil {
			t.Fatal(err)
		}
	}
	return f, p, private, public
}

func TestVoiceAgentFindsReadsAndPreparesWithoutExposingControlDirectory(t *testing.T) {
	f, p, private, _ := voiceAgentFixture(t)
	call := func(name, args string) string {
		t.Helper()
		r, e := p.Call(context.Background(), name, json.RawMessage(args))
		if e != nil {
			t.Fatal(name, e)
		}
		return r
	}
	r := call("voice_library_list", `{"offset":0,"limit":1}`)
	if !strings.Contains(r, `"total":2`) || !strings.Contains(r, `"has_more":true`) {
		t.Fatal(r)
	}
	r = call("voice_library_read", `{"voice_id":"owner-voice"}`)
	if !strings.Contains(r, `"project_id":"gpt-sovits"`) || !strings.Contains(r, "gpu=true") {
		t.Fatal("missing registered GPT-SoVITS inference route", r)
	}
	if !strings.Contains(r, private.Weights[0].File) || strings.Contains(r, f.manager.dir) {
		t.Fatal("control path leaked", r)
	}
	r = call("voice_library_prepare", `{"voice_id":"owner-voice"}`)
	var out struct {
		Directory string
		Global    bool
		Files     []struct{ Path string }
	}
	if json.Unmarshal([]byte(r), &out) != nil || !out.Global || out.Directory != filepath.Join(p.executionContext().Private, "voice-library") {
		t.Fatal(r)
	}
	if b, e := os.ReadFile(out.Files[0].Path); e != nil || string(b) != "synthetic owner-voice" {
		t.Fatal(string(b), e)
	}
	if _, e := p.Call(context.Background(), "voice_library_prepare", json.RawMessage(`{"voice_id":"owner-voice","file_ids":["../../admin-password"]}`)); e == nil {
		t.Fatal("unregistered file accepted")
	}
	p.trace.Sender = "ordinary-member"
	r = call("voice_library_list", `{}`)
	if strings.Contains(r, "owner-voice") || !strings.Contains(r, "public-voice") {
		t.Fatal(r)
	}
	if _, e := p.Call(context.Background(), "voice_library_read", json.RawMessage(`{"voice_id":"owner-voice"}`)); e == nil {
		t.Fatal("member read personal voice")
	}
	if _, e := p.Call(context.Background(), "music_service_start", json.RawMessage(`{}`)); e == nil {
		t.Fatal("member started host service")
	}
	r = call("voice_library_prepare", `{"voice_id":"public-voice"}`)
	if json.Unmarshal([]byte(r), &out) != nil || out.Global || !strings.HasPrefix(out.Directory, p.executionContext().Conversation) {
		t.Fatal(r)
	}
}

func TestVoiceAgentRejectsCorruptModelAndForeignSource(t *testing.T) {
	f, p, private, _ := voiceAgentFixture(t)
	if err := writePrivate(filepath.Join(f.manager.privateVoiceLibrary(), "files", private.Weights[0].File), []byte("corrupt")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Call(context.Background(), "voice_library_prepare", json.RawMessage(`{"voice_id":"owner-voice"}`)); err == nil {
		t.Fatal("corrupt model published")
	}
	p.trace.Account = "other-account"
	if _, err := p.Call(context.Background(), "voice_library_list", json.RawMessage(`{}`)); err == nil {
		t.Fatal("foreign account read library")
	}
}

func TestRoleFolderKeepsVoiceIDsAndOwnerDownloadsStable(t *testing.T) {
	f, p, private, _ := voiceAgentFixture(t)
	private.Role, private.Directory = "mom", "mom"
	private.Weights[0].Relative = "mom/weights/model.ckpt"
	root := f.manager.privateVoiceLibrary()
	file := filepath.Join(root, filepath.FromSlash(private.Weights[0].Relative))
	if err := ensureManaged(filepath.Dir(file)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "files", private.Weights[0].File), file); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"items": []voiceEntry{private}})
	if err := writePrivate(filepath.Join(root, "catalog.json"), b); err != nil {
		t.Fatal(err)
	}
	r, err := p.Call(context.Background(), "voice_library_prepare", json.RawMessage(`{"voice_id":"owner-voice"}`))
	if err != nil || !strings.Contains(r, strings.ReplaceAll(file, `\`, `\\`)) {
		t.Fatal(r, err)
	}
	directory, relative, err := f.manager.voiceLibraryObject(private.Weights[0].File)
	if err != nil || filepath.Join(directory, relative) != file {
		t.Fatal(directory, relative, err)
	}
	f.login()
	if code, body, _ := f.request("GET", "/api/voice-library/files/"+private.Weights[0].File, nil, false, false); code != 200 || string(body) != "synthetic owner-voice" {
		t.Fatal(code, string(body))
	}
	private.Weights[0].Relative = "../../foreign/private.ckpt"
	b, _ = json.Marshal(map[string]any{"items": []voiceEntry{private}})
	writePrivate(filepath.Join(root, "catalog.json"), b)
	if _, err = p.Call(context.Background(), "voice_library_prepare", json.RawMessage(`{"voice_id":"owner-voice"}`)); err == nil {
		t.Fatal("foreign role path accepted")
	}
}
