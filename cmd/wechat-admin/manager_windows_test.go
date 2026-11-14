package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"wechat-bot/internal/admin"
	"wechat-bot/internal/linuxnative"
)

// Any unexpected native method panics through the nil embedded interface.
// The production Windows constructor must not start a device or native work.
type retirementNative struct{ admin.Native }

func (retirementNative) Account() string { return "synthetic-retired-account" }
func (retirementNative) Ready() bool     { return false }
func (retirementNative) Status(context.Context) (linuxnative.Status, error) {
	return linuxnative.Status{Connection: "disconnected"}, nil
}

func TestWindowsManagerRetiresDeviceControl(t *testing.T) {
	manager, err := newManager(t.TempDir(), "", retirementNative{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	const origin = "http://retirement.invalid"
	const password = "synthetic-retirement-password"
	handler, err := admin.NewHandler(manager, password, origin)
	if err != nil {
		t.Fatal(err)
	}
	csrf := ""
	var cookies []*http.Cookie
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, origin+path, strings.NewReader(body))
		request.Header.Set("Origin", origin)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", csrf)
		for _, cookie := range cookies {
			request.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	login := request("POST", "/api/login", `{"password":"`+password+`"}`)
	var session struct {
		CSRF string `json:"csrf"`
	}
	if login.Code != http.StatusOK || json.Unmarshal(login.Body.Bytes(), &session) != nil || session.CSRF == "" {
		t.Fatal("synthetic owner login failed", login.Code, login.Body.String())
	}
	csrf, cookies = session.CSRF, login.Result().Cookies()
	status := request("GET", "/api/status", "")
	var state struct {
		Retired bool `json:"device_control_retired"`
	}
	if status.Code != http.StatusOK || json.Unmarshal(status.Body.Bytes(), &state) != nil || !state.Retired {
		t.Fatal("Windows entrypoint revived device execution", status.Code, status.Body.String())
	}
	for _, call := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/api/mobile/devices", "", http.StatusNotFound},
		{"POST", "/api/mobile/tasks", `{}`, http.StatusNotFound},
		{"POST", "/api/plugin-packages/mobile-control/runtime", `{"action":"start"}`, http.StatusGone},
	} {
		response := request(call.method, call.path, call.body)
		if response.Code != call.status {
			t.Fatalf("Windows entrypoint admitted retired action %s %s: %d %s", call.method, call.path, response.Code, response.Body.String())
		}
	}
	archive := request("GET", "/api/plugin-packages/mobile-control", "")
	if archive.Code != http.StatusOK || !strings.Contains(archive.Body.String(), `"archive":true`) {
		t.Fatal("read-only device archive disappeared", archive.Code, archive.Body.String())
	}
}
