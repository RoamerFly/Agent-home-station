package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicVoicePortableRegistrationAlsoBindsPrepareAndNamedResources(t *testing.T) {
	f, p, _, public := voiceAgentFixture(t)
	base := filepath.Join(p.executionContext().Public, "models", "voice-library")
	file := public.Weights[0]
	expected := file.Path
	relative, err := filepath.Rel(base, expected)
	if err != nil {
		t.Fatal(err)
	}
	file.Relative = strings.ReplaceAll(filepath.ToSlash(relative), "/", `\`)
	file.Path = "/mnt/e/obsolete-public/models/voice-library/model.ckpt"
	public.Weights[0] = file
	// This catalogue fixture uses the same ID as voiceAgentFixture so the
	// ordinary source-scoped tool path exercises real verified publication.
	b, _ := json.Marshal(map[string]any{"items": []voiceEntry{public}})
	if err = os.WriteFile(filepath.Join(base, "catalog.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := p.Call(t.Context(), "voice_library_prepare", json.RawMessage(`{"voice_id":"public-voice"}`))
	var prepared struct {
		Files []struct{ Path string } `json:"files"`
	}
	if err != nil || json.Unmarshal([]byte(raw), &prepared) != nil || len(prepared.Files) != 1 {
		t.Fatal(raw, err)
	}
	if body, err := os.ReadFile(prepared.Files[0].Path); err != nil || string(body) != "synthetic public-voice" {
		t.Fatal("prepared file changed", err)
	}
	public.Public = true
	if err = p.verifyNamedSpeechFiles(t.Context(), public, []voiceFile{file}); err != nil {
		t.Fatal("portable named resource rejected", err)
	}
	if path, err := p.namedSpeechFilePath(public, file); err != nil || path != expected {
		t.Fatal(path, err)
	}
	for _, changed := range []voiceFile{
		{Relative: `..\foreign.ckpt`, Hash: file.Hash, Bytes: file.Bytes},
		{Relative: file.Relative, Hash: strings.Repeat("b", 64), Bytes: file.Bytes},
	} {
		if err := p.verifyNamedSpeechFiles(context.Background(), public, []voiceFile{changed}); err == nil {
			t.Fatal("unverified named speech resource accepted")
		}
	}
	public.Weights[0].Relative = "../foreign.ckpt"
	b, _ = json.Marshal(map[string]any{"items": []voiceEntry{public}})
	if err = os.WriteFile(filepath.Join(base, "catalog.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Call(t.Context(), "voice_library_prepare", json.RawMessage(`{"voice_id":"public-voice"}`)); err == nil {
		t.Fatal("prepare escaped public resource root")
	}
	if f.native.loginCalls.Load() != 0 {
		t.Fatal("resource checks touched native WeChat")
	}
}

func publicVoiceObjectFixture(t *testing.T, relative string) (*webFixture, string, string, string, voiceFile) {
	t.Helper()
	f := setup(t)
	t.Setenv("WECHAT_WORKSPACE_ROOT", filepath.Join(f.dir, "managed-workspaces"))
	root := filepath.Join(f.manager.managedRoot(), "public_workspace", "models", "voice-library")
	path := filepath.Join(root, "character", "weights", "model.pth")
	if err := ensureManaged(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	data := []byte("synthetic-public-model")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	file := voiceFile{Filename: "model.pth", Hash: hash, Bytes: int64(len(data)), File: hash + ".pth", Relative: relative, Path: "/mnt/e/old-deployment/public_workspace/models/voice-library/character/weights/model.pth"}
	publicVoiceObjectCatalogue(t, root, file)
	f.login()
	return f, root, path, file.File, file
}

func publicVoiceObjectCatalogue(t *testing.T, root string, file voiceFile) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"version": 1, "items": []voiceEntry{{ID: "public-synthetic", Engine: "RVC", Weights: []voiceFile{file}}}})
	if err := os.WriteFile(filepath.Join(root, "catalog.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPublicVoiceObjectUsesPortableRegistrationAndVerifiedRange(t *testing.T) {
	for _, relative := range []string{"character/weights/model.pth", `character\weights\model.pth`} {
		t.Run(relative, func(t *testing.T) {
			f, _, _, name, _ := publicVoiceObjectFixture(t, relative)
			req, _ := http.NewRequest("GET", f.server.URL+"/api/voice-library/files/"+name, nil)
			req.Header.Set("Range", "bytes=0-3")
			resp, err := f.client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusPartialContent || string(body) != "synt" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal(resp.StatusCode, string(body), resp.Header)
			}
		})
	}
}

func TestPublicVoiceObjectRejectsEscapeAndChangedRegisteredContent(t *testing.T) {
	for _, kind := range []string{"parent", "windows-parent", "absolute", "unc", "hash", "size", "content", "unregistered"} {
		t.Run(kind, func(t *testing.T) {
			f, root, path, name, file := publicVoiceObjectFixture(t, "character/weights/model.pth")
			switch kind {
			case "parent":
				file.Relative = "../private/model.pth"
			case "windows-parent":
				file.Relative = `character\..\..\private\model.pth`
			case "absolute":
				file.Relative = `C:\private\model.pth`
			case "unc":
				file.Relative = `\\server\private\model.pth`
			case "hash":
				file.Hash = strings.Repeat("b", 64)
			case "size":
				file.Bytes++
			case "content":
				if err := os.WriteFile(path, []byte("Synthetic-public-model"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unregistered":
				name = strings.Repeat("c", 64) + ".pth"
			}
			publicVoiceObjectCatalogue(t, root, file)
			if code, _, _ := f.request("GET", "/api/voice-library/files/"+name, nil, false, false); code != 404 {
				t.Fatal("unverified public file served", code)
			}
		})
	}
}

func TestPublicVoiceObjectKeepsCurrentAbsoluteRegistrationAndRejectsLinks(t *testing.T) {
	f, root, path, name, file := publicVoiceObjectFixture(t, "")
	file.Path = path
	publicVoiceObjectCatalogue(t, root, file)
	if code, body, _ := f.request("GET", "/api/voice-library/files/"+name, nil, false, false); code != 200 || string(body) != "synthetic-public-model" {
		t.Fatal(code, string(body))
	}
	outside := filepath.Join(f.dir, "outside.pth")
	if err := os.WriteFile(outside, []byte("synthetic-public-model"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.pth")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("symlink unavailable; absolute registration already checked")
	}
	file.Relative = "link.pth"
	publicVoiceObjectCatalogue(t, root, file)
	if code, _, _ := f.request("GET", "/api/voice-library/files/"+name, nil, false, false); code != 404 {
		t.Fatal("public symlink served", code)
	}
}
