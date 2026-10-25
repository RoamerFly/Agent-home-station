package admin

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDefaultAdminPasswordAndManagedOverridePersist(t *testing.T) {
	dir := t.TempDir()
	value, err := Password(dir, "")
	if err != nil || value != "admin" {
		t.Fatal(value, err)
	}
	if err = writePrivate(filepath.Join(dir, "admin-password"), []byte("synthetic-new-password\n")); err != nil {
		t.Fatal(err)
	}
	value, err = Password(dir, "synthetic-env-seed")
	if err != nil || value != "synthetic-new-password" {
		t.Fatal("managed password reset at restart", err)
	}
	for _, invalid := range []string{"1234", " admin", "admin\n", "abc\x00def"} {
		if validPassword(invalid) {
			t.Fatal("invalid password accepted")
		}
	}
}
func TestChangingPasswordRevokesAllSessionsAndPreservesOtherData(t *testing.T) {
	f := setup(t)
	f.login()
	jar, _ := cookiejar.New(nil)
	second := &http.Client{Jar: jar}
	oldClient := f.client
	f.client = second
	f.login()
	secondCSRF := f.csrf
	f.client = oldClient
	// Both clients must have independent cookies; use the second token only with
	// its matching client when verifying rejection below.
	code, b, _ := f.request("GET", "/api/session", nil, false, false)
	if code != 200 {
		t.Fatal(code)
	}
	var firstSession struct {
		CSRF string `json:"csrf"`
	}
	json.Unmarshal(b, &firstSession)
	f.csrf = firstSession.CSRF
	beforeConfig := f.manager.Config()
	for _, attempt := range []struct {
		current, next string
		csrf          bool
		code          int
	}{{"test-password-at-least-24-bytes", "synthetic-new-password", false, 403}, {"wrong", "synthetic-new-password", true, 400}, {"test-password-at-least-24-bytes", "tiny", true, 400}} {
		code, _, _ := f.request("POST", "/api/password", map[string]string{"current_password": attempt.current, "new_password": attempt.next}, true, attempt.csrf)
		if code != attempt.code {
			t.Fatal(code, attempt.code)
		}
	}
	code, _, _ = f.request("POST", "/api/password", map[string]string{"current_password": "test-password-at-least-24-bytes", "new_password": "synthetic-new-password"}, true, true)
	if code != 200 {
		t.Fatal(code)
	}
	if code, _, _ := f.request("GET", "/api/config", nil, false, false); code != 401 {
		t.Fatal("current session survived password change", code)
	}
	f.client = second
	f.csrf = secondCSRF
	if code, _, _ := f.request("GET", "/api/config", nil, false, false); code != 401 {
		t.Fatal("other session survived password change", code)
	}
	if code, _, _ := f.request("POST", "/api/login", map[string]string{"password": "test-password-at-least-24-bytes"}, true, false); code != 401 {
		t.Fatal("old password accepted", code)
	}
	if code, _, _ := f.request("POST", "/api/login", map[string]string{"password": "synthetic-new-password"}, true, false); code != 200 {
		t.Fatal("new password failed", code)
	}
	if value, err := Password(f.dir, ""); err != nil || value != "synthetic-new-password" {
		t.Fatal("new password not durable", err)
	}
	if !reflect.DeepEqual(f.manager.Config(), beforeConfig) {
		t.Fatal("password change changed bot settings")
	}
}
func TestPasswordSaveFailureDoesNotInvalidateExistingCredential(t *testing.T) {
	f := setup(t)
	f.login()
	if err := os.Mkdir(filepath.Join(f.dir, "admin-password"), 0700); err != nil {
		t.Fatal(err)
	}
	code, _, _ := f.request("POST", "/api/password", map[string]string{"current_password": "test-password-at-least-24-bytes", "new_password": "synthetic-new-password"}, true, true)
	if code != 500 {
		t.Fatal(code)
	}
	if code, _, _ := f.request("GET", "/api/config", nil, false, false); code != 200 {
		t.Fatal("save failure revoked old session", code)
	}
	if code, _, _ := f.request("POST", "/api/login", map[string]string{"password": "test-password-at-least-24-bytes"}, true, false); code != 200 {
		t.Fatal("save failure lost old password", code)
	}
}
