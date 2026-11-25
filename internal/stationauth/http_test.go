package stationauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"wechat-bot/internal/websession"
)

func TestPasswordHTTPChange(t *testing.T) {
	dir := t.TempDir()
	owner, _ := websession.New("admin", "", "test_owner")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", owner.Login)
	RegisterPassword(mux, owner, dir)
	login := httptest.NewRequest("POST", "http://127.0.0.1/api/login", strings.NewReader(`{"password":"admin"}`))
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("Origin", "http://127.0.0.1")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, login)
	if w.Code != 200 {
		t.Fatalf("login %d", w.Code)
	}
	var session struct {
		CSRF string `json:"csrf"`
	}
	json.Unmarshal(w.Body.Bytes(), &session)
	cookie := w.Result().Cookies()[0]
	change := func(current, csrf string) int {
		r := httptest.NewRequest("POST", "http://127.0.0.1/api/password", strings.NewReader(`{"current_password":"`+current+`","new_password":"changed-password"}`))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(cookie)
		r.Header.Set("Origin", "http://127.0.0.1")
		r.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	if code := change("admin", ""); code != 403 {
		t.Fatalf("csrf %d", code)
	}
	if code := change("wrong", session.CSRF); code != 400 {
		t.Fatalf("wrong password %d", code)
	}
	if code := change("admin", session.CSRF); code != 200 {
		t.Fatalf("change %d", code)
	}
	if got, err := Password(dir, ""); err != nil || got != "changed-password" {
		t.Fatal("password not persisted", err)
	}
	if code := change("changed-password", session.CSRF); code != 401 {
		t.Fatalf("session survived %d", code)
	}
}
