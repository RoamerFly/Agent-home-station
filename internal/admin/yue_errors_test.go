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

func TestYuEFailureAuditRetainsFixedCauseWithoutUpstreamSecrets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		code   string
	}{{"offline_backend", 503, `{"token":"never-log-me"}`, "yue_backend_unavailable"}, {"auth", 401, `private-upstream-body`, "yue_bridge_auth_failed"}} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) }))
			defer upstream.Close()
			f := setup(t)
			f.manager.speech.YuEEnabled = true
			f.manager.speech.YuEEndpoint = upstream.URL
			f.manager.speech.YuEToken = "private-fixture-token"
			p, _, _ := f.manager.toolAccess()
			p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "failure-audit"})
			_, err := p.Call(context.Background(), "music_local_start", json.RawMessage(`{"title":"晚风归家","style":"gentle pop","lyrics":"[verse]\n晚风伴我归家","kind":"voice"}`))
			if err == nil {
				t.Fatal("failed tool succeeded")
			}
			found := false
			for _, row := range f.manager.Logs() {
				if row.Event == "tool_call_failed" {
					found = true
					if row.Tool != "music_local_start" || row.ToolCode != tc.code || strings.Contains(row.Reason, "never-log-me") || strings.Contains(row.Reason, "private-upstream-body") {
						t.Fatal("cause missing or secret leaked", row)
					}
				}
			}
			if !found {
				t.Fatal("failure audit missing")
			}
		})
	}
}
