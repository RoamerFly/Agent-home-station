package station

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/wechatui"
)

type canvasNative struct {
	stationWechatNative
	accounts map[string]string
}

func (n canvasNative) Inspect(_ context.Context, id string) (apphost.ConnectionStatus, error) {
	account, ok := n.accounts[id]
	if !ok {
		return apphost.ConnectionStatus{}, errors.New("unknown synthetic account")
	}
	return apphost.ConnectionStatus{Connection: apphost.Connection{ID: id, AccountID: account}, Runtime: "offline", Login: "signed_out"}, nil
}

func TestWechatCanvasLegacyEditorAccountScopeAndRevision(t *testing.T) {
	owner, err := websession.New("synthetic-local-password", "", "synthetic-canvas")
	if err != nil {
		t.Fatal(err)
	}
	one, two := wechatapp.ConnectionID("one"), wechatapp.ConnectionID("two")
	host := &stationWechat{ctx: context.Background(), root: t.TempDir(), workers: map[string]*stationWechatWorker{}, stationWechatNative: canvasNative{accounts: map[string]string{one: "one", two: "two"}}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", owner.Login)
	mux.HandleFunc("GET /canvas/{connection}", owner.Protect(host.canvasRoute))
	mux.HandleFunc("POST /canvas/{connection}", owner.Protect(host.canvasRoute))
	server := httptest.NewServer(mux)
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	f := &localHomeFixture{t: t, server: server, client: &http.Client{Jar: jar}}
	path := "/canvas/" + one
	if code, _ := f.request("GET", path, nil, false); code != 401 {
		t.Fatal("anonymous", code)
	}
	f.login()
	read := func(id string) wechatui.CanvasView {
		t.Helper()
		code, result := f.request("GET", "/canvas/"+id, nil, false)
		data, _ := json.Marshal(result)
		var view wechatui.CanvasView
		if code != 200 || json.Unmarshal(data, &view) != nil {
			t.Fatal(code, result)
		}
		return view
	}
	view := read(one)
	if view.Saved || len(view.Document.Nodes) != 24 {
		t.Fatal("legacy default missing", view)
	}
	if _, err := os.Stat(filepath.Join(host.root, one)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("opening diagram created an account runtime", err)
	}
	view.Document.Name = "合成旧版微信流程"
	view.Document.Nodes = append(view.Document.Nodes, wechatui.CanvasNode{ID: "custom-note", Kind: "note", Title: "合成说明", Tone: "teal", X: 12, Y: 34})
	input := map[string]any{"base_revision": view.Revision, "document": view.Document}
	if code, _ := f.request("POST", path, input, false); code != 403 {
		t.Fatal("CSRF", code)
	}
	if code, result := f.request("POST", path, input, true); code != 200 {
		t.Fatal(code, result)
	}
	saved := read(one)
	if !saved.Saved || saved.Revision == view.Revision || saved.Document.Name != view.Document.Name {
		t.Fatal("save not retained", saved)
	}
	if code, _ := f.request("POST", path, input, true); code != 409 {
		t.Fatal("stale revision", code)
	}
	if other := read(two); other.Saved || other.Document.Name == saved.Document.Name {
		t.Fatal("account leaked", other)
	}
	if code, _ := f.request("GET", "/canvas/unknown", nil, false); code != 404 {
		t.Fatal("unknown connection", code)
	}
	worker := host.workers[one]
	if worker.run != nil || worker.policy.Enabled || len(worker.policy.Grants) != 0 {
		t.Fatal("diagram changed operational authorization")
	}
	// Reopen the store and keep a damaged original byte-for-byte.
	host.workers = map[string]*stationWechatWorker{}
	if reopened := read(one); reopened.Revision != saved.Revision {
		t.Fatal("restart lost diagram")
	}
	canvasPath := filepath.Join(host.root, one, wechatCanvasFile)
	if err := os.WriteFile(canvasPath, []byte("synthetic damaged file"), 0600); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.request("GET", path, nil, false); code != 503 {
		t.Fatal("damaged file accepted", code)
	}
	if data, _ := os.ReadFile(canvasPath); string(data) != "synthetic damaged file" {
		t.Fatal("damaged original overwritten")
	}
}
