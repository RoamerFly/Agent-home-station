package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"wechat-bot/internal/companion"
)

func TestOfflineYuEPreflightDoesNotClaimOrCreateGeneration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{{"backend_offline", 200, `{"ready":false}`}, {"invalid", 200, `not-json`}, {"auth_failed", 401, `{"error":"unauthorized"}`}} {
		t.Run(tc.name, func(t *testing.T) {
			posts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					posts++
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			f := setup(t)
			{
				legacySpeechFixture := readLegacySpeechTestConfig(t, f.manager)
				legacySpeechFixture.YuEEnabled = true
				legacySpeechFixture.YuEEndpoint = server.URL
				legacySpeechFixture.YuEToken = "fixture-token"
				writeLegacySpeechMigrationFixture(t, f.manager, legacySpeechFixture)
			}
			installLegacyLocalMusicPlugin(t, f.manager)
			p, _, _ := f.manager.toolAccess()
			p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "offline-request"})
			_, err := p.callYuE(context.Background(), "music_local_start", json.RawMessage(`{"title":"晚风归家","style":"warm female pop","lyrics":"[verse]\n迎着晚风回家","duration_seconds":60,"kind":"voice"}`))
			if err == nil || posts != 0 || len(f.manager.agent.data.Operations) != 0 || len(f.manager.agent.list("test", "filehelper")) != 0 {
				t.Fatal("offline backend claimed or submitted a job", err, posts)
			}
		})
	}
}

func TestUnknownYuESubmissionDoesNotReportToolSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Write([]byte(`{"ready":true,"bridge_version":7}`))
			return
		}
		w.WriteHeader(503)
	}))
	defer server.Close()
	f := setup(t)
	{
		legacySpeechFixture := readLegacySpeechTestConfig(t, f.manager)
		legacySpeechFixture.YuEEnabled = true
		legacySpeechFixture.YuEEndpoint = server.URL
		legacySpeechFixture.YuEToken = "fixture-token"
		writeLegacySpeechMigrationFixture(t, f.manager, legacySpeechFixture)
	}
	installLegacyLocalMusicPlugin(t, f.manager)
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "unknown-request"})
	_, err := p.callYuE(context.Background(), "music_local_start", json.RawMessage(`{"title":"晚风归家","style":"warm female pop","lyrics":"[verse]\n迎着晚风回家","kind":"voice"}`))
	if err == nil || !strings.Contains(err.Error(), "未确认") || len(f.manager.agent.data.Operations) != 1 || len(f.manager.agent.list("test", "filehelper")) != 0 {
		t.Fatal("unknown submission accepted as generation", err)
	}
}

func TestYuEHealthRequiresAdminAndNeverLeaksToken(t *testing.T) {
	f := setup(t)
	if code, _, _ := f.request("GET", "/api/speech/yue-health", nil, true, true); code != 401 {
		t.Fatal(code)
	}
	f.login()
	code, b, _ := f.request("GET", "/api/speech/yue-health", nil, true, true)
	if code != 200 || !strings.Contains(string(b), "migration_required") || strings.Contains(string(b), "yue_token") {
		t.Fatal(code, string(b))
	}
}
