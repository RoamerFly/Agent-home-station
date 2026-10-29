package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

// Opt-in, ephemeral, synthetic-only browser fixture. Native SendText panics;
// no production configuration, login or model is connected.
func TestMemoryBrowserFixture(t *testing.T) {
	if os.Getenv("WECHAT_MEMORY_UI_FIXTURE") != "1" {
		t.Skip("opt-in synthetic browser fixture")
	}
	m, e := NewManager(t.TempDir(), "", &fakeNative{})
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	m.config.Companion.Permissions.Remember = true
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "synthetic@chatroom", Sender: "alice", SenderName: "合成用户", TraceID: "source-1", Text: "我喜欢红茶，不喝咖啡。"}
	if e = m.memory.Observe(in, m.config.Companion, time.Now()); e != nil {
		t.Fatal(e)
	}
	id := companion.ScopeID("wechat", "test", in.Thread)
	page, e := m.memory.SearchMemory(context.Background(), companion.MemoryQuery{Scopes: []string{id}, Originals: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.memory.SaveMemory(context.Background(), companion.MemoryEntry{Scope: id, Subject: "alice", Kind: "preference", Detail: "喜欢红茶，不喝咖啡。", Sources: []companion.MemorySource{{EventID: page.Items[0].Event.ID, Quote: in.Text}}}, 0); e != nil {
		t.Fatal(e)
	}
	h, e := NewHandler(m, "synthetic-browser-password-2026", "")
	if e != nil {
		t.Fatal(e)
	}
	stop := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fixture/stop" && r.Method == "POST" {
			once.Do(func() { close(stop) })
			w.WriteHeader(204)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer server.Close()
	t.Log("FIXTURE_URL=" + server.URL)
	select {
	case <-stop:
	case <-time.After(8 * time.Minute):
		t.Fatal("browser fixture timed out")
	}
}
