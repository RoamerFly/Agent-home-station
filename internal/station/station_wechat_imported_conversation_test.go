package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/websession"
)

type archiveReadFixtureNative struct {
	stationWechatNative
	sender *linuxnative.Sender
}

func (n archiveReadFixtureNative) Inspect(_ context.Context, id string) (apphost.ConnectionStatus, error) {
	account := n.sender.Account()
	if id != wechatapp.ConnectionID(account) {
		return apphost.ConnectionStatus{}, apphost.ErrNotFound
	}
	return apphost.ConnectionStatus{Connection: apphost.Connection{ID: id, AccountID: account}, Login: "authenticated"}, nil
}

func (n archiveReadFixtureNative) WorkerSender(_ context.Context, id string) (*linuxnative.Sender, error) {
	if id != wechatapp.ConnectionID(n.sender.Account()) {
		return nil, apphost.ErrNotFound
	}
	return n.sender, nil
}

func TestWechatImportedConversationHTTPAuthorizationAndContinuation(t *testing.T) {
	const account, thread = "synthetic-archive-account", "synthetic-archive-room@chatroom"
	dir := t.TempDir()
	now := time.Now().UTC()
	grant := now.Add(-time.Minute)
	id := wechatapp.ConnectionID(account)
	store, err := companion.NewStore(filepath.Join(dir, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	instance := store.SourceInstanceID()
	store.Close()
	token := filepath.Join(dir, "synthetic-token")
	if err := os.WriteFile(token, []byte("synthetic-token"), 0600); err != nil {
		t.Fatal(err)
	}
	native, err := linuxnative.New(linuxnative.Config{URL: "http://127.0.0.1:1", Account: account, TokenFile: token})
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("合成原话", 100)
	sum := sha256.Sum256([]byte(body))
	event := companion.LegacyMemoryEvent{InstanceID: instance, EventID: "synthetic-event", MessageID: "synthetic-message",
		ServerID: "synthetic-server", Sender: "synthetic-sender", OccurredAt: now.Add(-time.Hour).Format(time.RFC3339Nano),
		Revision: 1, Text: body, Hash: hex.EncodeToString(sum[:])}
	snapshot := stationLegacyMemory{Version: 1, Account: account, Thread: thread, Grant: grant, Digest: strings.Repeat("a", 64),
		Selection: companion.LegacyMemorySelection{Events: []companion.LegacyMemoryEvent{event}}}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, stationLegacyMemoryFile(account, thread)), raw, 0600); err != nil {
		t.Fatal(err)
	}
	cursorCodec, err := newStationContactCursor()
	if err != nil {
		t.Fatal(err)
	}
	worker := &stationWechatWorker{dir: dir, policy: stationWechatPolicy{HistoryGrants: map[string]time.Time{thread: grant}}}
	host := &stationWechat{ctx: t.Context(), stationWechatNative: archiveReadFixtureNative{sender: native}, workers: map[string]*stationWechatWorker{id: worker}, contactCursor: cursorCodec}
	owner, err := websession.New("synthetic-local-password", "", "synthetic-archive")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", owner.Login)
	mux.HandleFunc("GET /api/apps/wechat/connections/{connection}/conversations/{thread}/imported-memory", owner.Protect(host.readImportedConversation(owner)))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	jar, _ := cookiejar.New(nil)
	f := &localHomeFixture{t: t, server: server, client: &http.Client{Jar: jar}}
	base := "/api/apps/wechat/connections/" + id + "/conversations/"
	path := base + thread + "/imported-memory?kind=events&max_bytes=256"
	if code, _ := f.request(http.MethodGet, path, nil, false); code != 401 {
		t.Fatalf("anonymous archive status = %d", code)
	}
	f.login()
	code, first := f.request(http.MethodGet, path, nil, false)
	if code != 200 || first["returned_count"] != float64(1) || first["next_cursor"] == "" {
		t.Fatalf("first archive page: %d %#v", code, first)
	}
	cursor := url.QueryEscape(first["next_cursor"].(string))
	code, next := f.request(http.MethodGet, path+"&cursor="+cursor, nil, false)
	if code != 200 || next["items"].([]any)[0].(map[string]any)["text_offset"].(float64) <= 0 {
		t.Fatalf("continued archive page: %d %#v", code, next)
	}
	if code, _ := f.request(http.MethodGet, base+"other@chatroom/imported-memory?cursor="+cursor, nil, false); code != 403 {
		t.Fatalf("cross-thread archive status = %d", code)
	}
	if code, _ := f.request(http.MethodGet, strings.Replace(path, "kind=events", "kind=memories", 1)+"&cursor="+cursor, nil, false); code != 409 {
		t.Fatalf("cross-kind cursor status = %d", code)
	}
	worker.mu.Lock()
	delete(worker.policy.HistoryGrants, thread)
	worker.mu.Unlock()
	if code, _ := f.request(http.MethodGet, path+"&cursor="+cursor, nil, false); code != 403 {
		t.Fatalf("revoked archive status = %d", code)
	}
}
