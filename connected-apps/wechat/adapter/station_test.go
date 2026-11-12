package adapter

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"

	"wechat-bot/internal/apphost"
)

func TestStationRegistrationVerifiesOwnedDaemonAndScopesReads(t *testing.T) {
	data := t.TempDir()
	native := filepath.Join(data, "apps", "wechat", "native")
	if err := os.MkdirAll(native, 0700); err != nil {
		t.Fatal(err)
	}
	const token = "synthetic-station-native-token"
	if err := os.WriteFile(filepath.Join(native, "api-token"), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	var replaceAfterList, replaced atomic.Bool
	var lists atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token || r.Method != http.MethodGet {
			t.Error("native request had the wrong method or ownership credential")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/status":
			generation := "original-generation"
			if replaced.Load() {
				generation = "new-generation"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"pid": 4321, "dataDir": native, "serverId": "fixture", "ownershipGeneration": generation})
		case "/v1/accounts":
			lists.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
				{"id": "first", "alias": "profile-one", "authenticated": true, "connection": "online", "token": token},
				{"id": "second", "alias": "profile-two", "authenticated": false, "connection": "offline"},
				{"id": DefaultAccountID, "alias": DefaultAccountID, "authenticated": false, "connection": "offline"},
			}})
			if replaceAfterList.Load() {
				replaced.Store(true)
			}
		default:
			t.Error("request escaped read-only native endpoints", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	if _, err := StationRegistration(t.Context(), server.URL, 1234, data); err == nil {
		t.Fatal("mismatching PID admitted")
	}
	wrong := t.TempDir()
	wrongNative := filepath.Join(wrong, "apps", "wechat", "native")
	if err := os.MkdirAll(wrongNative, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wrongNative, "api-token"), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := StationRegistration(t.Context(), server.URL, 4321, wrong); err == nil {
		t.Fatal("mismatching private data root admitted")
	}
	app, err := StationRegistration(t.Context(), server.URL, 4321, data)
	if err != nil || app.ID != "wechat" || app.Registration != "station-native" || lists.Load() != 0 {
		t.Fatal(app.Application, err)
	}
	if _, ok := app.Provider.(apphost.LifecycleController); ok {
		t.Fatal("native observation granted worker mutation")
	}
	connections, err := app.Provider.Connections(t.Context())
	if err != nil || len(connections) != 3 || connections[0].ID != ConnectionID("first") || connections[1].Label != "profile-two" || connections[2].AccountID != DefaultAccountID {
		t.Fatal(connections, err)
	}
	for _, tc := range []struct{ id, login, runtime string }{
		{"first", "authenticated", "online"},
		{"second", "signed_out", "offline"},
	} {
		got, err := app.Provider.Inspect(t.Context(), ConnectionID(tc.id))
		if err != nil || got.AccountID != tc.id || got.Login != tc.login || got.Runtime != tc.runtime || got.Worker != "unavailable" || len(got.SupportedActions) != 0 {
			t.Fatal(got, err)
		}
	}
	if _, err := app.Provider.Inspect(t.Context(), ConnectionID("foreign")); !errors.Is(err, apphost.ErrNotFound) {
		t.Fatal("foreign connection resolved", err)
	}
	replaceAfterList.Store(true)
	if items, err := app.Provider.Connections(t.Context()); !errors.Is(err, apphost.ErrUnavailable) || len(items) != 0 {
		t.Fatal("replacement daemon released stale accounts", items, err)
	}
}

func TestStationConnectionsPrepareDefaultDesktop(t *testing.T) {
	data := t.TempDir()
	native := filepath.Join(data, "apps", "wechat", "native")
	if err := os.MkdirAll(native, 0700); err != nil {
		t.Fatal(err)
	}
	const token = "synthetic-default-token"
	if err := os.WriteFile(filepath.Join(native, "api-token"), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	created, creates := atomic.Bool{}, atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		write := func(value any) { _ = json.NewEncoder(w).Encode(value) }
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/status":
			write(map[string]any{"pid": 6789, "dataDir": native, "serverId": "default-fixture", "ownershipGeneration": "generation"})
		case "GET /v1/accounts":
			items := []map[string]any{{"id": "existing", "alias": "existing", "authenticated": false, "connection": "offline"}}
			if created.Load() {
				items = append(items, map[string]any{"id": DefaultAccountID, "alias": DefaultAccountID, "authenticated": false, "connection": "offline"})
			}
			write(map[string]any{"items": items})
		case "GET /v1/accounts/" + DefaultAccountID:
			if !created.Load() {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			write(map[string]any{"id": DefaultAccountID, "alias": DefaultAccountID})
		case "POST /v1/accounts":
			var input map[string]string
			if json.NewDecoder(r.Body).Decode(&input) != nil || input["id"] != DefaultAccountID {
				t.Error("default account was not requested")
			}
			creates.Add(1)
			created.Store(true)
			w.WriteHeader(http.StatusCreated)
			write(map[string]any{"id": DefaultAccountID, "alias": DefaultAccountID})
		default:
			t.Errorf("unexpected native request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	app, err := StationRegistration(t.Context(), server.URL, 6789, data)
	if err != nil {
		t.Fatal(err)
	}
	setup, ok := app.Provider.(apphost.AccountSetup)
	if !ok {
		t.Fatal("station provider lost account setup")
	}
	first, err := setup.ProvisionConnection(t.Context(), DefaultProvisionRequestID)
	if err != nil || first.AccountID != DefaultAccountID {
		t.Fatal(first, err)
	}
	second, err := setup.ProvisionConnection(t.Context(), DefaultProvisionRequestID)
	if err != nil || second.AccountID != DefaultAccountID || creates.Load() != 1 {
		t.Fatal(second, err, creates.Load())
	}
}

func TestStationNativeWindowsExtendedDirectory(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path syntax")
	}
	if !sameDaemonDirectory(`\\?\E:\Station\native`, `e:\station\native`) || !sameDaemonDirectory(`\\?\UNC\server\share\native`, `\\server\share\native`) {
		t.Fatal("Win32 canonical path rejected")
	}
	if !sameDaemonDirectory(`\\?\E:\Station\native`, `\\?\E:\Station\native`) {
		t.Fatal("same extended ownership path rejected on reread")
	}
	if sameDaemonDirectory(`\\?\E:\Station\native`, `E:\other\native`) {
		t.Fatal("different private directory accepted")
	}
}
