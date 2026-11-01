package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mediaspec"
)

func TestLegacySpeechCompatibilityRoutesAreMigrationOnly(t *testing.T) {
	f := setup(t)
	f.login()
	c := mediaspec.Defaults()
	c.Key = "synthetic.private.api.key"
	c.YuEToken = "synthetic.yue.token"
	c.YuEEndpoint = "http://127.0.0.1:6188"
	c.YuEEnabled = true
	c.TTSEnabled = true
	c.ASREnabled = true
	path := filepath.Join(f.dir, "speech-config.json")
	if err := os.WriteFile(path, []byte(`{"legacy":"migration-source-only"}`), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := f.request("POST", "/api/speech", c, true, false); code != 403 {
		t.Fatal("csrf", code)
	}
	code, body, _ := f.request("POST", "/api/speech", c, true, true)
	if code != 409 || !bytes.Contains(body, []byte("speech_legacy_configuration_migration_required")) || bytes.Contains(body, []byte(c.Key)) || bytes.Contains(body, []byte(c.YuEToken)) {
		t.Fatal("legacy write accepted or leaked credential", code, string(body))
	}
	code, body, _ = f.request("GET", "/api/speech", nil, false, false)
	if code != 200 || !bytes.Contains(body, []byte(`"migration_required":true`)) || bytes.Contains(body, []byte("synthetic")) {
		t.Fatal("legacy configuration exposed")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("legacy POST modified migration source", err)
	}
	stored := readLegacySpeechTestConfig(t, f.manager)
	if stored.Key != "" || stored.YuEToken != "" || stored.YuEEnabled {
		t.Fatal("legacy POST changed loaded state", stored)
	}
	// Existing migration data remains readable by the server-side preview; this
	// route deliberately neither changes nor authorizes it.
	writeLegacySpeechMigrationFixture(t, f.manager, c)
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "one"})
	if len(p.yueTools()) != 0 {
		t.Fatal("legacy speech settings authorized music without an installed plugin")
	}
	installLegacyLocalMusicPlugin(t, f.manager)
	if len(p.yueTools()) != 8 {
		t.Fatal("local tools missing")
	}
	if _, err := p.callYuE(context.Background(), "music_local_status", json.RawMessage(`{"job_id":"foreign-job"}`)); err == nil {
		t.Fatal("foreign job inspected")
	}
	p.trace.Preview = true
	if len(p.yueTools()) != 0 {
		t.Fatal("preview allowed effectful song")
	}
}
