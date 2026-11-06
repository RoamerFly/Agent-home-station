package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startSyntheticAccount(t *testing.T, app *SearchApp, revision string) (string, string) {
	t.Helper()
	result, err := app.AuthorizeAccount(context.Background(), "owner/session", AccountAuthorizationInput{
		Action: "begin", Revision: revision, ClientKey: "fixture-client", ClientSecret: "fixture-private-secret",
		RedirectURI: "https://owner.example.test/callback",
	})
	if err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(result.(accountOAuthStart).AuthorizeURL)
	if err != nil || link.Query().Get("state") == "" {
		t.Fatalf("invalid authorization link: %v", err)
	}
	query := url.Values{"code": {"synthetic-code"}, "state": {link.Query().Get("state")}, "scopes": {"user_info"}}
	return "https://owner.example.test/callback?" + query.Encode(), link.Query().Get("state")
}

func TestAccountAuthorizationPersistsOnlyVerifiedConnection(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	app := New(dir)
	t.Cleanup(func() {
		app.accountMu.Lock()
		defer app.accountMu.Unlock()
		app.clearAccountAttempt()
	})
	ctx := context.Background()
	now := time.Now().UTC()
	app.now = func() time.Time { return now }
	calls := 0
	app.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		switch r.URL.Path {
		case "/oauth/access_token/":
			return jsonResponse(`{"message":"success","data":{"error_code":0,"open_id":"fixture-open-id","scope":"user_info","access_token":"fixture-access-token","refresh_token":"fixture-refresh-token","expires_in":3600,"refresh_expires_in":7200}}`), nil
		case "/oauth/userinfo/":
			return jsonResponse(`{"err_no":0,"data":{"error_code":"0","open_id":"fixture-open-id","nickname":" 合成测试账号 "}}`), nil
		}
		t.Fatalf("unexpected official request: %s", r.URL.Path)
		return nil, nil
	})
	callback, state := startSyntheticAccount(t, app, "")
	complete := func(binding, callback string) (any, error) {
		return app.AuthorizeAccount(ctx, binding, AccountAuthorizationInput{Action: "complete", CallbackURL: callback})
	}
	for _, input := range []struct{ binding, callback string }{
		{"other/session", callback},
		{"owner/session", strings.Replace(callback, "owner.example.test", "other.example.test", 1)},
		{"owner/session", strings.Replace(callback, state, "forged", 1)},
		{"owner/session", callback + "&state=" + state},
	} {
		if _, err := complete(input.binding, input.callback); !errors.Is(err, ErrAccountCallback) {
			t.Fatalf("forged authorization: %v", err)
		}
	}
	if _, err := complete("owner/session", strings.Replace(callback, "scopes=user_info", "scopes=other", 1)); !errors.Is(err, ErrAccountScope) {
		t.Fatalf("invalid scope: %v", err)
	}
	if calls != 0 {
		t.Fatal("invalid callback called the platform")
	}
	result, err := complete("owner/session", callback)
	if err != nil {
		t.Fatal(err)
	}
	view := result.(AccountAuthorizationView)
	if !view.Configured || view.Label != "合成测试账号" || view.Status != "ready" || view.Revision == "" || calls != 2 {
		t.Fatalf("authorization state=%+v requests=%d", view, calls)
	}
	encoded, _ := json.Marshal(view)
	for _, private := range []string{"fixture-access-token", "fixture-open-id", "fixture-private-secret", "fixture-client", "synthetic-code", state} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("private value reached management response")
		}
	}
	if _, err := complete("owner/session", callback); !errors.Is(err, ErrAccountCallback) || calls != 2 {
		t.Fatalf("replayed callback: calls=%d err=%v", calls, err)
	}
	saved, err := os.ReadFile(filepath.Join(dir, accountFile))
	if err != nil || !strings.Contains(string(saved), "fixture-access-token") || strings.Contains(string(saved), "fixture-refresh-token") ||
		strings.Contains(string(saved), "fixture-private-secret") || strings.Contains(string(saved), "synthetic-code") {
		t.Fatalf("private file shape or permissions: %v", err)
	}
	restarted := New(dir)
	restarted.now = func() time.Time { return now }
	if restored, err := restarted.AccountAuthorization(); err != nil || restored != view {
		t.Fatalf("restored authorization: %+v %v", restored, err)
	}
	now = now.Add(2 * time.Hour)
	if expired, err := restarted.AccountAuthorization(); err != nil || expired.Status != "expired" {
		t.Fatalf("expired authorization: %+v %v", expired, err)
	}
	if _, err := restarted.AuthorizeAccount(ctx, "owner/session", AccountAuthorizationInput{Action: "disconnect", Revision: ""}); !errors.Is(err, ErrAccountRevision) {
		t.Fatalf("stale disconnect: %v", err)
	}
	result, err = restarted.AuthorizeAccount(ctx, "owner/session", AccountAuthorizationInput{Action: "disconnect", Revision: view.Revision})
	if err != nil || result.(AccountAuthorizationView).Configured {
		t.Fatalf("disconnect: %v %v", result, err)
	}
	saved, err = os.ReadFile(filepath.Join(dir, accountFile))
	if err != nil || strings.Contains(string(saved), "fixture-access-token") || strings.Contains(string(saved), "fixture-open-id") {
		t.Fatalf("revocation retained credentials: %v", err)
	}
}

func TestAccountAuthorizationDisconnectWinsOverLateVerification(t *testing.T) {
	app := New(t.TempDir())
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	app.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/oauth/access_token/" {
			close(entered)
			<-release
			return jsonResponse(`{"message":"success","data":{"error_code":0,"open_id":"late-account","scope":"user_info","access_token":"late-access","refresh_token":"late-refresh","expires_in":3600,"refresh_expires_in":7200}}`), nil
		}
		return jsonResponse(`{"err_no":0,"data":{"error_code":"0","open_id":"late-account","nickname":"迟到授权"}}`), nil
	})
	callback, _ := startSyntheticAccount(t, app, "")
	completed := make(chan error, 1)
	go func() {
		_, err := app.AuthorizeAccount(ctx, "owner/session", AccountAuthorizationInput{Action: "complete", CallbackURL: callback})
		completed <- err
	}()
	select {
	case <-entered:
	case err := <-completed:
		t.Fatalf("expected dispatch before result: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("expected code exchange dispatch")
	}
	_, err := app.AuthorizeAccount(ctx, "owner/session", AccountAuthorizationInput{Action: "disconnect"})
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-completed; !errors.Is(err, ErrAccountRevision) {
		t.Fatalf("late verification: %v", err)
	}
	view, err := app.AccountAuthorization()
	if err != nil || view.Configured {
		t.Fatalf("late callback restored access: %+v %v", view, err)
	}
}
