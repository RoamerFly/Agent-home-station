package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/linuxnative"
)

func TestLocalWechatWorkersIsolatePauseRevokeAndRestore(t *testing.T) {
	base := t.TempDir()
	host, workspace := filepath.Join(base, "host"), filepath.Join(base, "workspace")
	nativeRoot := filepath.Join(host, "apps", "wechat", "native")
	if err := os.MkdirAll(nativeRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nativeRoot, "api-token"), []byte("synthetic-worker-native-token"), 0600); err != nil {
		t.Fatal(err)
	}
	var nativeMu sync.Mutex
	messages := map[string][]linuxnative.Message{}
	sends := map[string][]string{}
	submitKeys := map[string]string{}
	var foreignReads atomic.Int32
	var replaced, lateAccepted atomic.Bool
	var replacementTraffic atomic.Int32
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nativeMu.Lock()
		defer nativeMu.Unlock()
		if r.Header.Get("Authorization") != "Bearer synthetic-worker-native-token" {
			t.Error("missing worker credential")
			w.WriteHeader(401)
			return
		}
		out := func(value any) { _ = json.NewEncoder(w).Encode(value) }
		switch r.URL.Path {
		case "/v1/status":
			gen := "worker-generation"
			if replaced.Load() {
				gen = "replaced-worker-generation"
			}
			out(map[string]any{"pid": 7654, "dataDir": nativeRoot, "serverId": "worker-station", "ownershipGeneration": gen})
			return
		case "/v1/accounts":
			out(map[string]any{"items": []map[string]any{
				{"id": "one", "authenticated": true, "connection": "online"},
				{"id": "two", "authenticated": true, "connection": "online"},
			}})
			return
		}
		if replaced.Load() {
			replacementTraffic.Add(1)
		}
		fields := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/accounts/"), "/")
		account := fields[0]
		if account != "one" && account != "two" {
			t.Error("unexpected account", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		path := strings.Join(fields[1:], "/")
		switch {
		case r.Method == http.MethodGet && path == "":
			out(map[string]any{"id": account, "authenticated": true, "connection": "online"})
		case r.Method == http.MethodGet && path == "events":
			after, _ := strconv.Atoi(r.URL.Query().Get("after"))
			events := []linuxnative.Event{}
			for i, message := range messages[account] {
				if i < after {
					continue
				}
				event := linuxnative.Event{Cursor: strconv.Itoa(i + 1), Kind: "message.committed"}
				event.Data.MessageID, event.Data.ThreadID, event.Data.Direction = message.ID, message.ThreadID, message.Direction
				events = append(events, event)
			}
			out(linuxnative.EventPage{Items: events, NextCursor: strconv.Itoa(after + len(events))})
		case r.Method == http.MethodGet && strings.HasPrefix(path, "messages/"):
			for _, m := range messages[account] {
				if m.ID == strings.TrimPrefix(path, "messages/") {
					if m.ThreadID == "foreign-thread" {
						foreignReads.Add(1)
					}
					out(m)
					return
				}
			}
			w.WriteHeader(404)
		case r.Method == http.MethodPost && path == "sends":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			thread, _ := body["threadId"].(string)
			sends[account] = append(sends[account], thread)
			submitKeys[account] = r.Header.Get("Idempotency-Key")
			w.WriteHeader(202)
			out(map[string]any{"id": "send-" + account, "accountId": account, "threadId": thread, "kind": "text", "state": "queued"})
		case r.Method == http.MethodGet && strings.HasPrefix(path, "sends"):
			if account != "two" || submitKeys[account] == "" {
				w.WriteHeader(404)
				return
			}
			row := map[string]any{"id": "send-two", "accountId": account, "threadId": "person-two", "kind": "text", "state": "queued"}
			if lateAccepted.Load() {
				row["state"], row["serverId"] = "accepted", "67001"
			}
			out(row)
		default:
			// Native display-name discovery is optional. Return an empty page.
			if r.Method == http.MethodGet {
				out(map[string]any{"items": []any{}})
			} else {
				t.Error("unexpected mutation", r.Method, r.URL.Path)
				w.WriteHeader(404)
			}
		}
	}))
	defer daemon.Close()
	appendMessage := func(account, thread, text string) {
		nativeMu.Lock()
		defer nativeMu.Unlock()
		index := len(messages[account]) + 1
		messages[account] = append(messages[account], linuxnative.Message{
			ID: fmt.Sprintf("%s-msg-%d", account, index), AccountID: account, ThreadID: thread, SenderID: thread,
			ServerID: strconv.Itoa(62000 + index), Direction: "incoming", Type: 1,
			Body: "/ai " + text, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		})
	}
	reopen := func() (*LocalHomeHandler, *localHomeFixture) {
		t.Helper()
		entry, err := wechatapp.StationRegistration(t.Context(), daemon.URL, 7654, host)
		if err != nil {
			t.Fatal(err)
		}
		registry, err := apphost.New(entry)
		if err != nil {
			t.Fatal(err)
		}
		handler, err := NewLocalHomeHandlerWithApps(host, workspace, "synthetic-local-password", "", registry)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		t.Cleanup(func() { _ = handler.Close() })
		jar, _ := cookiejar.New(nil)
		f := &localHomeFixture{t: t, server: server, client: &http.Client{Jar: jar}}
		f.login()
		return handler, f
	}
	h, f := reopen()
	blockStarted, blockStopped := make(chan struct{}, 1), make(chan struct{}, 1)
	h.wechat.model = localModelFunc(func(ctx context.Context, _ string, history []map[string]string) (string, error) {
		raw, _ := json.Marshal(history)
		if strings.Contains(string(raw), "block-synthetic-first") {
			blockStarted <- struct{}{}
			<-ctx.Done()
			blockStopped <- struct{}{}
			return "", ctx.Err()
		}
		return `{"action":"reply","messages":["合成回复"]}`, nil
	})
	path := func(account string) string { return "/api/apps/wechat/connections/" + wechatapp.ConnectionID(account) }
	request := func(f *localHomeFixture, method, path string, body any, csrf bool, code int) map[string]any {
		t.Helper()
		got, result := f.request(method, path, body, csrf)
		if got != code {
			t.Fatalf("%s %s: status %d want %d, %#v", method, path, got, code, result)
		}
		return result
	}
	policy := func(f *localHomeFixture, account string, threads ...string) {
		t.Helper()
		request(f, "PUT", path(account)+"/policy", map[string]any{"reply_threads": append([]string{}, threads...)}, true, 200)
	}
	lifecycle := func(f *localHomeFixture, account, action string, code int) map[string]any {
		t.Helper()
		return request(f, "POST", path(account)+"/lifecycle", map[string]string{"action": action}, true, code)
	}
	state := func(f *localHomeFixture, account string) map[string]any {
		t.Helper()
		return request(f, "GET", path(account), nil, false, 200)["connection"].(map[string]any)
	}
	waitState := func(f *localHomeFixture, account, expected string) {
		t.Helper()
		waitAppLifecycle(t, func() bool { return state(f, account)["worker"] == expected })
	}
	await := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(14 * time.Second):
			t.Fatal("worker did not reach expected stage")
		}
	}
	lifecycle(f, "one", "start", 409)
	request(f, "PUT", path("one")+"/policy", map[string]any{"reply_threads": []string{"person-one"}}, false, 403)
	request(f, "PUT", path("one")+"/policy", map[string]any{"reply_threads": nil}, true, 400)
	request(f, "PUT", path("other")+"/policy", map[string]any{"reply_threads": []string{"person-one"}}, true, 404)
	policy(f, "one", "person-one")
	policy(f, "two", "person-two")
	settings := defaultLocalModelSettings()
	settings.BaseURL = "https://127.0.0.1:1"
	if _, err := h.chat.models.Save(LocalModelInput{LocalModelSettings: settings, APIKey: "synthetic-worker-model-not-real"}); err != nil {
		t.Fatal(err)
	}
	lifecycle(f, "one", "start", 200)
	lifecycle(f, "two", "start", 200)
	waitState(f, "one", "running")
	waitState(f, "two", "running")
	appendMessage("one", "foreign-thread", "must-not-read")
	appendMessage("one", "person-one", "block-synthetic-first")
	await(blockStarted)
	appendMessage("two", "person-two", "synthetic-second")
	waitAppLifecycle(t, func() bool {
		nativeMu.Lock()
		defer nativeMu.Unlock()
		return len(sends["two"]) != 0
	})
	if row := lifecycle(f, "one", "stop", 200); row["worker"] != "paused" || row["submissions_paused"] != true {
		t.Fatal(row)
	}
	await(blockStopped)
	if detail := state(f, "one"); detail["login"] != "authenticated" || detail["runtime"] != "online" || detail["worker"] != "paused" {
		t.Fatal(detail)
	}
	waitState(f, "two", "running")
	nativeMu.Lock()
	receiptKey := submitKeys["two"]
	nativeMu.Unlock()
	if receiptKey == "" {
		t.Fatal("missing stable delivery key")
	}
	policy(f, "two")
	waitState(f, "two", "paused")
	lateAccepted.Store(true)
	waitAppLifecycle(t, func() bool {
		code, row := f.request("GET", path("two")+"/receipts/"+receiptKey, nil, false)
		return code == 200 && row["state"] == "accepted"
	})
	lifecycle(f, "two", "start", 409)
	request(f, "GET", path("two")+"/receipts/"+receiptKey, nil, false, 200)
	request(f, "GET", path("one")+"/receipts/"+receiptKey, nil, false, 404)
	listed := request(f, "GET", path("two")+"/receipts?limit=1", nil, false, 200)
	items, _ := listed["items"].([]any)
	if len(items) != 1 || listed["coverage"] != "current_worker_checkpoint" || listed["snapshot"] == "" {
		t.Fatal("missing scoped delivery list", listed)
	}
	item := items[0].(map[string]any)
	if item["idempotency_key"] != receiptKey || item["conversation_id"] != "person-two" || item["lookup"] != "stored" {
		t.Fatal("delivery list lost original task identity", listed)
	}
	request(f, "GET", path("two")+"/receipts?limit=0", nil, false, 400)
	request(f, "GET", path("two")+"/receipts?after=../foreign", nil, false, 400)
	request(f, "GET", path("two")+"/receipts?after="+receiptKey, nil, false, 200)
	anonymous, err := http.Get(f.server.URL + path("two") + "/receipts")
	if err != nil {
		t.Fatal(err)
	}
	anonymous.Body.Close()
	if anonymous.StatusCode != 401 {
		t.Fatal("anonymous receipt listing", anonymous.StatusCode)
	}
	// Regranting the same thread requires a new request. The interrupted
	// earlier message must not be regenerated using the new authorization.
	policy(f, "one")
	policy(f, "one", "person-one")
	lifecycle(f, "one", "start", 200)
	waitState(f, "one", "running")
	select {
	case <-blockStarted:
		t.Fatal("revoked source was regenerated on regrant")
	case <-time.After(3 * time.Second):
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	h2, f2 := reopen()
	waitState(f2, "one", "running")
	waitState(f2, "two", "paused")
	request(f2, "GET", path("two")+"/receipts/"+receiptKey, nil, false, 200)
	sender, err := h2.wechat.WorkerSender(t.Context(), wechatapp.ConnectionID("one"))
	if err != nil {
		t.Fatal(err)
	}
	replaced.Store(true)
	if _, err := sender.Status(t.Context()); err == nil {
		t.Fatal("existing worker admitted an account request after daemon replacement")
	}
	if err := h2.Close(); err != nil {
		t.Fatal(err)
	}
	if replacementTraffic.Load() != 0 {
		t.Fatal("worker contacted an unverified account endpoint")
	}
	nativeMu.Lock()
	defer nativeMu.Unlock()
	if foreignReads.Load() != 0 || len(sends["one"]) != 0 || len(sends["two"]) != 1 || sends["two"][0] != "person-two" {
		t.Fatal("conversation or delivery escaped its worker scope", foreignReads.Load(), sends)
	}
}
