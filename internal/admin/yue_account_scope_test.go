package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"wechat-bot/internal/companion"
)

func TestYuERequestsUseNativeAccountRatherThanQuotedTrace(t *testing.T) {
	f := setup(t)
	want := accountDirectory(f.manager.native.Account())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Wechat-Agent-Account") != want {
			t.Error("missing server-derived account binding")
		}
		w.Header().Set("X-Wechat-Agent-Account", want)
		w.Write([]byte(`{"ready":true,"bridge_version":9}`))
	}))
	defer server.Close()
	f.manager.speech.YuEEnabled = true
	f.manager.speech.YuEEndpoint = server.URL
	f.manager.speech.YuEToken = "synthetic-scope-token"
	installLegacyLocalMusicPlugin(t, f.manager)
	p := &permittedTools{manager: f.manager, trace: companion.Input{Account: "quoted-other-account"}}
	if _, _, e := p.yueRequest(legacyLocalMusicAdminContext(t, f.manager), "GET", "/health", nil, ""); e != nil {
		t.Fatal(e)
	}
}

func TestYuEWrongAccountStopsBeforeClaimingOrSubmittingGeneration(t *testing.T) {
	for _, withHeader := range []bool{true, false} {
		t.Run(map[bool]string{true: "response_header", false: "error_body"}[withHeader], func(t *testing.T) {
			posts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					posts++
				}
				if withHeader {
					w.Header().Set("X-Wechat-Agent-Account", accountDirectory("other"))
				}
				w.WriteHeader(http.StatusConflict)
				w.Write([]byte(`{"error":"music_account_scope_mismatch","private":"not-to-be-returned"}`))
			}))
			defer server.Close()
			f := setup(t)
			f.manager.speech.YuEEnabled = true
			f.manager.speech.YuEEndpoint = server.URL
			f.manager.speech.YuEToken = "synthetic-scope-token"
			installLegacyLocalMusicPlugin(t, f.manager)
			p, _, _ := f.manager.toolAccess()
			p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", Sender: "owner", TraceID: "wrong-account-request"})
			health := p.probeYuE(context.Background())
			if health.Ready || health.State != "account_scope_mismatch" {
				t.Fatal(health)
			}
			_, e := p.callYuE(context.Background(), "music_local_start", json.RawMessage(`{"title":"晚风归家","style":"warm female pop","lyrics":"[verse]\n迎着晚风回家","duration_seconds":60,"kind":"voice"}`))
			_, code := safeToolFailure(e)
			if code != "yue_account_scope_mismatch" || posts != 0 || len(f.manager.agent.data.Operations) != 0 || len(f.manager.agent.list("test", "filehelper")) != 0 {
				t.Fatal(e, posts)
			}
		})
	}
}
