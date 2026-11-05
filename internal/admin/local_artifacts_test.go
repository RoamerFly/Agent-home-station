package admin

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wechat-bot/internal/companion"
)

func TestLocalArtifactsPublishThroughChatAndRetainVerifiedSnapshot(t *testing.T) {
	f := localChatFixture(t)
	const body = "a synthetic finished document"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	calls := 0
	var id string
	f.handler.chat.modelFactory = func() (companion.LanguageModel, error) {
		return localModelFunc(func(_ context.Context, _ string, history []map[string]string) (string, error) {
			calls++
			switch calls {
			case 1:
				return fmt.Sprintf(`{"action":"tool","tool":"server_write","arguments":{"path":"Project Tools/finished.txt","content":%q}}`, body), nil
			case 2:
				return fmt.Sprintf(`{"action":"tool","tool":"artifact_publish","arguments":{"path":"Project Tools/finished.txt","expected_sha256":%q}}`, hash), nil
			default:
				for _, turn := range history {
					start := strings.Index(turn["content"], "work-")
					if start >= 0 && len(turn["content"]) >= start+69 {
						id = turn["content"][start : start+69]
					}
				}
				return `{"action":"reply","messages":["作品已登记。"]}`, nil
			}
		}), nil
	}
	if code, out := localChatSend(f, "publish-work", "写一份文档并登记成品"); code != 200 {
		t.Fatal(code, out)
	}
	if !artifactID(id, "work-") {
		t.Fatal("chat did not receive verified artifact ID", id)
	}
	artifact, err := f.handler.home.artifacts.read(id)
	if err != nil || artifact.Source == nil || artifact.Request != "publish-work" {
		t.Fatal("artifact lost source", artifact, err)
	}
	_, home := f.request("GET", "/api/local/home", nil, false)
	path := filepath.Join(home["directory"].(string), "Project Tools", "finished.txt")
	if err := os.WriteFile(path, []byte("changed later"), 0600); err != nil {
		t.Fatal(err)
	}
	original, err := f.handler.home.artifacts.open(t.Context(), artifact, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(original)
	original.Close()
	if err != nil || string(got) != body {
		t.Fatal("snapshot followed mutable source", string(got), err)
	}
	reused, err := f.handler.home.artifacts.publish(t.Context(), localArtifactInput{Path: "Project Tools/finished.txt", Expected: hash, Key: "publish-work"}, artifact.Source)
	if err != nil || reused.ID != id {
		t.Fatal("retry lost immutable artifact", reused, err)
	}
	f.server.Close()
	if err := f.handler.Close(); err != nil {
		t.Fatal(err)
	}
	f = newLocalHomeFixture(t, f.dir, f.root)
	f.login()
	if _, err := f.handler.home.artifacts.read(id); err != nil {
		t.Fatal("restart lost source or artifact", err)
	}
	for _, invalid := range []string{"../outside.txt", filepath.Join(f.dir, "fish-identity.json")} {
		code, _ := f.request("POST", "/api/local/artifacts", map[string]string{"path": invalid, "request_key": "invalid-path"}, true)
		if code != 400 {
			t.Fatal("private path accepted", code)
		}
	}
	if code, _ := f.request("POST", "/api/local/artifacts", map[string]string{"path": "Project Tools/finished.txt", "expected_sha256": hash, "request_key": "mismatch"}, true); code != 400 {
		t.Fatal("changed hash accepted", code)
	}
	private := filepath.Join(f.dir, "artifacts", id, "original")
	if err := os.WriteFile(private, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if file, err := f.handler.home.artifacts.open(t.Context(), artifact, func() bool { return true }); err == nil {
		file.Close()
		t.Fatal("corrupt snapshot accepted")
	}
	_, current := f.request("GET", "/api/local/home", nil, false)
	if code, out := f.request("POST", "/api/local/home", map[string]any{"action": "pause", "revision": current["revision"]}, true); code != 200 {
		t.Fatal(code, out)
	}
	if _, err := f.handler.home.artifacts.read(id); err == nil {
		t.Fatal("home pause did not stop access")
	}
	_, paused := f.request("GET", "/api/local/home", nil, false)
	if code, out := f.request("POST", "/api/local/home", map[string]any{"action": "authorize", "revision": paused["revision"]}, true); code != 200 {
		t.Fatal(code, out)
	}
	store, in, err := f.handler.chat.conversation()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Clear(companion.ScopeID("local", in.Account, in.Thread)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.handler.home.artifacts.read(id); err == nil {
		t.Fatal("forgotten source remained publishable")
	}
}
