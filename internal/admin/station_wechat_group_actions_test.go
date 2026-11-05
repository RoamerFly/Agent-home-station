package admin

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
)

func TestStationWechatGroupDirectoryAndMuteReceipt(t *testing.T) {
	base := t.TempDir()
	host, workspace := filepath.Join(base, "host"), filepath.Join(base, "home")
	nativeDir := filepath.Join(host, "apps", "wechat", "native")
	if err := os.MkdirAll(nativeDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nativeDir, "api-token"), []byte("synthetic-group-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	const group = "room-one@chatroom"
	const lostGroup = "room-lost@chatroom"
	var submissions, receiptReads atomic.Int32
	var ready, wrongTarget, nativeOffline atomic.Bool
	var unsupported atomic.Bool
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := func(value any) { _ = json.NewEncoder(w).Encode(value) }
		if r.Header.Get("Authorization") != "Bearer synthetic-group-fixture" {
			t.Error("native credentials not bound")
			w.WriteHeader(401)
			return
		}
		if nativeOffline.Load() {
			w.WriteHeader(503)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/status":
			out(map[string]any{"pid": 8008, "dataDir": nativeDir, "serverId": "synthetic-group", "ownershipGeneration": "first"})
		case "GET /v1/accounts":
			out(map[string]any{"items": []map[string]any{
				{"id": "one", "authenticated": true, "connection": "online"},
				{"id": "two", "authenticated": true, "connection": "online"},
			}})
		case "GET /v1/accounts/one/capabilities", "GET /v1/accounts/two/capabilities":
			targets := []string{}
			if !unsupported.Load() {
				targets = append(targets, "group")
			}
			out(map[string]any{"threadNotificationTargets": targets})
		case "GET /v1/accounts/one/groups":
			if q := r.URL.Query(); q.Get("limit") != "200" || q.Get("search") != "测试" {
				t.Error("incorrect native directory query", q)
			}
			out(map[string]any{"items": []map[string]any{{"id": group, "roomId": group, "displayName": "测试群", "announcement": "synthetic-private-announcement"}},
				"coverage": map[string]any{"partial": true, "databases": []string{"synthetic-private-db"}}, "nextCursor": "synthetic-native-group-cursor"})
		case "GET /v1/accounts/one/groups/" + group:
			out(map[string]any{"id": group, "roomId": group, "muted": ready.Load(), "isInGroup": true})
		case "GET /v1/accounts/one/groups/" + lostGroup:
			out(map[string]any{"id": lostGroup, "roomId": lostGroup})
		case "GET /v1/accounts/two/groups/" + group:
			w.WriteHeader(404)
		case "PUT /v1/accounts/one/threads/" + group + "/notifications":
			var in struct{ Muted *bool }
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.Muted == nil || !strings.HasPrefix(r.Header.Get("Idempotency-Key"), "station-mute-") {
				t.Error("mute submission is not bound to the requested value and idempotency key")
			}
			submissions.Add(1)
			receipt := syntheticStationMuteReceipt("queued", "not_attempted", group)
			if in.Muted != nil && !*in.Muted {
				receipt["id"] = "native-unmute"
			}
			out(receipt)
		case "PUT /v1/accounts/one/threads/" + lostGroup + "/notifications":
			submissions.Add(1)
			w.WriteHeader(503)
		case "GET /v1/accounts/one/operations/native-mute":
			receiptReads.Add(1)
			target := group
			if wrongTarget.Load() {
				target = lostGroup
			}
			state, outcome := "running", "not_attempted"
			if ready.Load() {
				state, outcome = "succeeded", "accepted"
			}
			out(syntheticStationMuteReceipt(state, outcome, target))
		default:
			t.Error("unexpected native call", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(daemon.Close)
	open := func() *localHomeFixture {
		t.Helper()
		entry, err := wechatapp.StationRegistration(t.Context(), daemon.URL, 8008, host)
		if err != nil {
			t.Fatal(err)
		}
		registry, err := apphost.New(entry)
		if err != nil {
			t.Fatal(err)
		}
		h, err := NewLocalHomeHandlerWithApps(host, workspace, "synthetic-local-password", "", registry)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(h)
		t.Cleanup(func() { _ = h.Close() })
		t.Cleanup(server.Close)
		jar, _ := cookiejar.New(nil)
		return &localHomeFixture{t: t, server: server, client: &http.Client{Jar: jar}, handler: h}
	}
	f := open()
	basePath := func(account string) string { return "/api/apps/wechat/connections/" + wechatapp.ConnectionID(account) }
	one, two := basePath("one"), basePath("two")
	path := one + "/group-actions"
	notify := one + "/groups/" + group + "/notification"
	request := func(method, path string, body any, csrf bool, expected int) map[string]any {
		t.Helper()
		status, out := f.request(method, path, body, csrf)
		if status != expected {
			t.Fatalf("%s %s = %d want %d, %v", method, path, status, expected, out)
		}
		return out
	}
	mute := func(target, key string, enable bool) map[string]any {
		return map[string]any{"group_id": target, "request_key": key, "muted": enable}
	}
	request("GET", one+"/groups", nil, false, 401)
	f.login()
	request("POST", "/api/local/home", map[string]string{"action": "initialize"}, true, 200)
	page := request("GET", one+"/groups?search=%E6%B5%8B%E8%AF%95&limit=500", nil, false, 200)
	raw, _ := json.Marshal(page)
	if !strings.Contains(string(raw), "测试群") || strings.Contains(string(raw), "synthetic-private") || strings.Contains(string(raw), "synthetic-native-group-cursor") ||
		page["effective_limit"] != float64(200) || page["partial"] != true {
		t.Fatal("directory exposed private fields or lost its boundaries", page)
	}
	request("GET", one+"/contacts?search=%E6%B5%8B%E8%AF%95&cursor="+page["next_cursor"].(string), nil, false, 400)
	if state := request("GET", notify, nil, false, 200); state["muted"] != false || state["operation"] != nil {
		t.Fatal("initial notification did not report the cached state", state)
	}
	request("POST", path, mute(group, "first", true), false, 403)
	request("POST", path, mute("not-a-group", "first", true), true, 400)
	request("POST", two+"/group-actions", mute(group, "first", true), true, 404)
	unsupported.Store(true)
	if state := request("GET", notify, nil, false, 200); state["can_change"] != false {
		t.Fatal("unsupported runtime advertised an action", state)
	}
	request("POST", path, mute(group, "first", true), true, 409)
	unsupported.Store(false)
	if submissions.Load() != 0 {
		t.Fatal("unauthorized mute reached native")
	}
	first := request("POST", path, mute(group, "first", true), true, 202)
	operation := first["operation_id"].(string)
	if first["state"] != "submitted_pending" {
		t.Fatal("queued result claimed as confirmed", first)
	}
	request("POST", path, mute(group, "first", true), true, 202)
	request("POST", path, mute(group, "first", false), true, 409)
	request("POST", path, mute(group, "another-key", false), true, 409)
	request("GET", two+"/operations/"+operation, nil, false, 404)
	wrongTarget.Store(true)
	ready.Store(true)
	if r := request("GET", one+"/operations/"+operation, nil, false, 200); r["state"] != "submitted_pending" || r["lookup"] != "identity_mismatch" {
		t.Fatal("foreign target receipt accepted", r)
	}
	wrongTarget.Store(false)
	if r := request("GET", notify, nil, false, 200); r["operation"].(map[string]any)["state"] != "succeeded" || r["muted"] != true {
		t.Fatal("late native receipt was not reconciled", r)
	}
	if r := request("POST", path, mute(group, "unmute", false), true, 202); r["state"] != "submitted_pending" || r["platform_operation_id"] != "native-unmute" {
		t.Fatal("confirmed mute blocked an explicit request to unmute", r)
	}
	unknown := request("POST", path, mute(lostGroup, "lost", true), true, 202)
	if unknown["state"] != "submission_unknown" || unknown["platform_operation_id"] != nil {
		t.Fatal("lost operation was misreported", unknown)
	}
	request("POST", path, mute(lostGroup, "lost", true), true, 202)
	request("POST", path, mute(lostGroup, "lost-new-key", false), true, 409)
	request("POST", one+"/lifecycle", map[string]string{"action": "stop"}, true, 200)
	request("POST", path, mute(group, "paused-new-key", false), true, 409)
	if submissions.Load() != 3 || receiptReads.Load() != 2 {
		t.Fatal("duplicate submission or unexpected lookup", submissions.Load(), receiptReads.Load())
	}
	f.server.Close()
	if err := f.handler.Close(); err != nil {
		t.Fatal(err)
	}
	f = open()
	f.login()
	nativeOffline.Store(true)
	if r := request("GET", one+"/operations/"+operation, nil, false, 200); r["state"] != "succeeded" || r["lookup"] != "stored" {
		t.Fatal("confirmed receipt lost after restart", r)
	}
	if r := request("GET", one+"/groups/"+lostGroup+"/notification", nil, false, 200); r["operation"].(map[string]any)["state"] != "submission_unknown" || r["can_change"] != false {
		t.Fatal("offline recovery lost unknown reservation", r)
	}
}

func syntheticStationMuteReceipt(state, outcome, target string) map[string]any {
	return map[string]any{"id": "native-mute", "accountId": "one", "kind": "group_mutation", "state": state,
		"result": map[string]string{"action": "mute", "threadId": target, "targetId": target, "remoteOutcome": outcome}}
}
