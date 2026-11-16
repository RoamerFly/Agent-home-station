package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"testing"
)

func TestDesktopConsoleRestoresSessionWithoutPasswordPage(t *testing.T) {
	t.Setenv("WECHAT_LEGACY_AUTOLOGIN", "")
	args := os.Args
	os.Args = []string{"wechat-admin.exe", "--desktop"}
	t.Cleanup(func() { os.Args = args })
	f := setup(t)

	code, data, _ := f.request(http.MethodGet, "/api/session", nil, false, false)
	var session struct {
		CSRF string `json:"csrf"`
	}
	if code != http.StatusOK || json.Unmarshal(data, &session) != nil || session.CSRF == "" {
		t.Fatalf("desktop session was not established: %d %s", code, data)
	}
	firstCSRF := session.CSRF
	code, page, _ := f.request(http.MethodGet, "/", nil, false, false)
	if code != http.StatusOK {
		t.Fatalf("console: %d", code)
	}
	for _, removed := range []string{"login-view", "login-form", "login-password", "admin-password-panel", "id=\"logout\""} {
		if bytes.Contains(page, []byte(removed)) {
			t.Fatalf("removed password UI still served: %s", removed)
		}
	}
	if code, _, _ := f.request(http.MethodGet, "/api/config", nil, false, false); code != http.StatusOK {
		t.Fatal("desktop console cannot access its account configuration")
	}
	if code, _, _ := f.request(http.MethodPost, "/api/wechat/login", map[string]any{}, true, false); code != http.StatusForbidden || f.native.loginCalls.Load() != 0 {
		t.Fatal("desktop entry bypassed mutation CSRF protection")
	}
	// The root navigation renewed the cookie; read its current CSRF before
	// revoking it to exercise the same recovery path as a service restart.
	_, data, _ = f.request(http.MethodGet, "/api/session", nil, false, false)
	if err := json.Unmarshal(data, &session); err != nil {
		t.Fatal(err)
	}
	f.csrf = session.CSRF
	if code, _, _ := f.request(http.MethodPost, "/api/logout", map[string]any{}, true, true); code != http.StatusOK {
		t.Fatal("could not revoke test session")
	}
	code, data, _ = f.request(http.MethodGet, "/api/session", nil, false, false)
	if code != http.StatusOK || json.Unmarshal(data, &session) != nil || session.CSRF == "" || session.CSRF == firstCSRF || session.CSRF == f.csrf {
		t.Fatalf("desktop session was not renewed: %d %s", code, data)
	}
	request, err := http.NewRequest(http.MethodGet, f.server.URL+"/api/session", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "foreign.example"
	response, err := f.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatal("desktop session admitted a foreign Host")
	}
}
