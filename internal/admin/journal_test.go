package admin

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wechat-bot/internal/bot"
)

type readyJournalNative struct{ *fakeNative }

func (*readyJournalNative) Ready() bool { return true }
func TestMCPReloadRetainsPrivateProviderCredential(t *testing.T) {
	f := setup(t)
	f.login()
	f.manager.native = &readyJournalNative{f.native}
	config := defaults()
	config.Enabled = true
	config.Companion.Enabled = true
	config.APIKey = "private-reload-key-not-returned"
	f.manager.mu.Lock()
	f.manager.config = config
	f.manager.mu.Unlock()
	if code, _, _ := f.request("POST", "/api/mcp", mcpDefaults(), true, true); code != 200 {
		t.Fatal("save", code)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		state := f.manager.BotStatus().State
		if state == "error" {
			t.Fatal("reload started from credential-redacted view")
		}
		if state == "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if f.manager.BotStatus().State != "running" {
		t.Fatal("worker did not recover")
	}
	data, err := os.ReadFile(filepath.Join(f.dir, "provider-key"))
	if err != nil || string(data) != config.APIKey {
		t.Fatal("runtime credential lost")
	}
	if _, data, _ := f.request("GET", "/api/config", nil, false, false); bytes.Contains(data, []byte(config.APIKey)) {
		t.Fatal("credential exposed")
	}
}
func TestMessageJournalPersistsOwnerRequestedContentAndSources(t *testing.T) {
	f := setup(t)
	f.login()
	f.manager.config.GroupRules = []GroupRule{{ID: "room@chatroom", Name: "测试日志群", Mode: "mention"}}
	f.manager.config.APIKey = "private-credential-to-redact"
	f.manager.record(bot.Record{Event: "message_received", Category: "received", ThreadID: "room@chatroom", Content: "收到的正文 private-credential-to-redact", Sender: "sender", Status: "received"})
	f.manager.record(bot.Record{Event: "message_sent", Category: "sent", ThreadID: "filehelper", Content: "发送的正文", Status: "accepted", Origin: "bot"})
	copy, err := NewManager(f.dir, "", f.native)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	entries := copy.Logs()
	if len(entries) != 2 || entries[0].Source != "测试日志群" || entries[1].Content != "发送的正文" {
		t.Fatal("journal lost source/content")
	}
	if bytes.Contains([]byte(entries[0].Content), []byte(f.manager.config.APIKey)) {
		t.Fatal("configured provider key recorded")
	}
	code, data, _ := f.request("GET", "/api/logs", nil, false, false)
	if code != 200 || !bytes.Contains(data, []byte("收到的正文")) || !bytes.Contains(data, []byte("source_kind")) {
		t.Fatal("missing message activity API")
	}
}

func TestModelAttemptDiagnosticsPersistThroughJournalAPIAndRestart(t *testing.T) {
	f := setup(t)
	f.login()
	f.manager.record(bot.Record{Event: "model_failed", Category: "error", ThreadID: "room@chatroom", MessageID: "failed-request", Reason: "model_service_unavailable", Status: "failed", ModelHTTPStatus: 503, ModelAttempts: 2, ModelMaxAttempts: 2, ModelStage: "http_status", ModelRetryDelayMS: 1000, DurationMS: 2000})
	copy, err := NewManager(f.dir, "", f.native)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	entries := copy.Logs()
	if len(entries) != 1 || entries[0].ModelAttempts != 2 || entries[0].ModelHTTPStatus != 503 || entries[0].ModelStage != "http_status" {
		t.Fatal("persisted diagnostics missing", entries)
	}
	code, data, _ := f.request("GET", "/api/logs", nil, false, false)
	if code != 200 || !bytes.Contains(data, []byte(`"model_attempts":2`)) || !bytes.Contains(data, []byte(`"model_http_status":503`)) {
		t.Fatal("API stripped request diagnostics", code)
	}
}
