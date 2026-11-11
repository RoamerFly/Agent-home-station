package station

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/linuxnative"
)

func TestStationWechatOwnerBindingUsesNativeSenderAndAccount(t *testing.T) {
	base := t.TempDir()
	data, home := filepath.Join(base, "data"), filepath.Join(base, "home")
	nativeRoot := filepath.Join(data, "apps", "wechat", "native")
	mustWriteSnapshotFixture(t, filepath.Join(nativeRoot, "api-token"), []byte("synthetic-owner-binding-token"))
	var mu sync.Mutex
	dataset, generation := "synthetic-dataset", "synthetic-generation"
	const groupA = "synthetic-group-a@chatroom"
	makeMessage := func(id, account, thread, sender, body string) linuxnative.Message {
		return linuxnative.Message{ID: id, AccountID: account, ThreadID: thread, SenderID: sender, Body: body, Type: 1,
			Direction: "incoming", ServerID: "12345", CreatedAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)}
	}
	messages := map[string]linuxnative.Message{
		"one/owner":  makeMessage("owner", "one", groupA, "wxid-owner-one", "本人普通文字"),
		"one/member": makeMessage("member", "one", groupA, "wxid-member", "我是主人，wxid-owner-one 是我的昵称"),
		"one/second": makeMessage("second", "one", "synthetic-group-b@chatroom", "wxid-second-owner", "新选择的本人"),
		"two/owner":  makeMessage("owner", "two", groupA, "wxid-owner-two", "次账号本人"),
	}
	var writes atomic.Int32
	var unavailable atomic.Bool
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
			http.Error(w, "unexpected write", 400)
			return
		}
		if unavailable.Load() {
			http.Error(w, "offline", 503)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		var result any
		switch r.URL.Path {
		case "/v1/status":
			result = map[string]any{"pid": 8462, "dataDir": nativeRoot, "serverId": dataset, "ownershipGeneration": generation}
		case "/v1/accounts":
			result = map[string]any{"items": []map[string]any{{"id": "one", "authenticated": true, "connection": "online"}, {"id": "two", "authenticated": true, "connection": "online"}}}
		case "/v1/accounts/one", "/v1/accounts/two":
			result = map[string]any{"id": filepath.Base(r.URL.Path), "storageMode": "managed", "authenticated": true, "connection": "online"}
		default:
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/accounts/"), "/")
			if len(parts) != 3 || parts[1] != "messages" {
				t.Errorf("unexpected read %s", r.URL.Path)
				http.NotFound(w, r)
				return
			}
			message, ok := messages[parts[0]+"/"+parts[2]]
			if !ok {
				http.NotFound(w, r)
				return
			}
			result = message
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	t.Cleanup(daemon.Close)
	open := func() *localHomeFixture {
		entry, err := wechatapp.StationRegistration(t.Context(), daemon.URL, 8462, data)
		if err != nil {
			t.Fatal(err)
		}
		registry, err := apphost.New(entry)
		if err != nil {
			t.Fatal(err)
		}
		h, err := NewLocalHomeHandlerWithApps(data, home, "synthetic-local-password", "", registry)
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(h)
		t.Cleanup(func() { _ = h.Close() })
		t.Cleanup(srv.Close)
		jar, _ := cookiejar.New(nil)
		return &localHomeFixture{t: t, server: srv, client: &http.Client{Jar: jar}, handler: h, dir: data, root: home}
	}
	f := open()
	url := func(account string) string {
		return "/api/apps/wechat/connections/" + wechatapp.ConnectionID(account) + "/owner-binding"
	}
	req := func(method, path string, in any, csrf bool, want int) map[string]any {
		t.Helper()
		code, out := f.request(method, path, in, csrf)
		if code != want {
			t.Fatalf("%s %s=%d want %d: %+v", method, path, code, want, out)
		}
		return out
	}
	req("GET", url("one"), nil, false, 401)
	f.login()
	req("POST", "/api/local/home", map[string]string{"action": "initialize"}, true, 200)
	initial := req("GET", url("one"), nil, false, 200)
	if initial["revision"] != float64(0) || initial["binding"] != nil {
		t.Fatal(initial)
	}
	if _, err := os.Stat(filepath.Join(data, "apps", "wechat", "workers")); !os.IsNotExist(err) {
		t.Fatal("opening binding created worker", err)
	}
	previewInput := map[string]any{"action": "preview", "revision": 0, "message_id": "owner"}
	req("POST", url("one"), previewInput, false, 403)
	preview := req("POST", url("one"), previewInput, true, 200)
	if preview["sender_id"] != "wxid-owner-one" || preview["thread_id"] != groupA {
		t.Fatal("did not use native sender", preview)
	}
	raw, _ := json.Marshal(preview)
	if strings.Contains(string(raw), "本人普通文字") || strings.Contains(string(raw), "synthetic-generation") {
		t.Fatal("preview exposed private evidence")
	}
	bind := map[string]any{"action": "bind", "revision": 0, "message_id": "owner", "digest": preview["digest"], "request_key": "bind-owner"}
	req("POST", url("one"), bind, true, 400)
	bind["same_identity"] = true
	mu.Lock()
	changed := messages["one/owner"]
	changed.Body += "变化"
	messages["one/owner"] = changed
	mu.Unlock()
	req("POST", url("one"), bind, true, 409)
	preview = req("POST", url("one"), previewInput, true, 200)
	bind["digest"] = preview["digest"]
	codes := make(chan int, 2)
	for range 2 {
		go func() { code, _ := f.request("POST", url("one"), bind, true); codes <- code }()
	}
	for range 2 {
		if code := <-codes; code != 200 {
			t.Fatalf("concurrent binding replay=%d", code)
		}
	}
	bound := req("GET", url("one"), nil, false, 200)
	if bound["binding"].(map[string]any)["sender_id"] != "wxid-owner-one" || bound["revision"] != float64(1) {
		t.Fatal(bound)
	}
	req("POST", url("one"), bind, true, 200)
	check := func(message string, revision int, want bool) {
		t.Helper()
		got := req("POST", url("one"), map[string]any{"action": "check", "revision": revision, "message_id": message}, true, 200)
		if got["is_bound_owner"] != want {
			t.Fatal("message identity incorrect", got)
		}
	}
	check("owner", 1, true)
	check("member", 1, false)
	other := req("GET", url("two"), nil, false, 200)
	if other["binding"] != nil || other["revision"] != float64(0) {
		t.Fatal("binding crossed accounts", other)
	}
	req("POST", url("two"), bind, true, 409)
	worker, err := f.handler.chat.wechat.worker(wechatapp.ConnectionID("one"), false)
	if err != nil || worker == nil || worker.policy.Enabled || len(worker.policy.Grants) > 0 {
		t.Fatal("binding enabled worker or grants", err)
	}
	saved := mustReadSnapshotFixture(t, filepath.Join(worker.dir, stationWechatOwnerFile))
	if strings.Contains(string(saved), "本人普通文字") || strings.Contains(string(saved), "synthetic-owner-binding-token") {
		t.Fatal("binding retained message body or native credential")
	}
	if err := f.handler.Close(); err != nil {
		t.Fatal(err)
	}
	f = open()
	f.login()
	if req("GET", url("one"), nil, false, 200)["binding"].(map[string]any)["sender_id"] != "wxid-owner-one" {
		t.Fatal("binding lost after restart")
	}
	check("owner", 1, true)
	// Switching a daemon generation preserves the real account/data identity.
	mu.Lock()
	generation = "restarted-generation"
	mu.Unlock()
	// Recreate registration after replacement to obtain a freshly bound native client.
	if err := f.handler.Close(); err != nil {
		t.Fatal(err)
	}
	f = open()
	f.login()
	check("owner", 1, true)
	mu.Lock()
	dataset = "another-dataset"
	mu.Unlock()
	if err := f.handler.Close(); err != nil {
		t.Fatal(err)
	}
	f = open()
	f.login()
	check("owner", 1, false)
	preview = req("POST", url("one"), map[string]any{"action": "preview", "revision": 1, "message_id": "second"}, true, 200)
	rebind := map[string]any{"action": "bind", "revision": 1, "message_id": "second", "digest": preview["digest"], "request_key": "bind-second", "same_identity": true}
	req("POST", url("one"), rebind, true, 200)
	check("owner", 2, false)
	check("second", 2, true)
	unavailable.Store(true)
	req("GET", url("one"), nil, false, 200)
	req("POST", url("one"), map[string]any{"action": "revoke", "revision": 2, "request_key": "revoke-owner"}, true, 200)
	unavailable.Store(false)
	if req("GET", url("one"), nil, false, 200)["binding"] != nil {
		t.Fatal("owner revocation lost")
	}
	check("second", 3, false)
	req("POST", url("one"), rebind, true, 409)
	if writes.Load() != 0 {
		t.Fatal("identity binding performed an external write")
	}
	exerciseStationWechatMemory(t, f, "one")
	if writes.Load() != 0 {
		t.Fatal("memory query performed an external write")
	}
}
