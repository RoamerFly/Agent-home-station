package admin

import (
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
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "synthetic@chatroom", IsGroup: true, Sender: "alice", SenderName: "合成用户"}
	id := companion.ScopeID(in.Channel, in.Account, in.Thread)
	at := time.Now().UTC().Add(-time.Hour)
	observe := func(trace, words string) []companion.MemorySource {
		t.Helper()
		in.TraceID, in.Text = trace, words
		at = at.Add(time.Minute)
		if err := m.memory.Observe(in, m.config.Companion, at); err != nil {
			t.Fatal(err)
		}
		scope, err := m.memory.Read(id, m.config.Companion, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		for _, msg := range scope.Messages {
			if msg.TraceID == trace && msg.EventID != "" {
				return []companion.MemorySource{{EventID: msg.EventID, Quote: words}}
			}
		}
		t.Fatalf("missing synthetic source %s", trace)
		return nil
	}
	tea := observe("source-1", "我喜欢红茶，不喝咖啡。")
	if _, e = m.memory.SaveMemory(t.Context(), companion.MemoryEntry{Scope: id, Subject: in.Sender, Kind: "preference", Detail: "喜欢红茶，不喝咖啡。", Sources: tea}, 0); e != nil {
		t.Fatal(e)
	}
	plan := observe("postcard", "我们约好周日一起画海边明信片。")
	if _, e = m.memory.SaveCommitment(t.Context(), companion.MemoryEntry{Scope: id, Subject: in.Sender, CommitmentState: "open", Detail: "周日一起画海边明信片", Sources: plan}); e != nil {
		t.Fatal(e)
	}
	observe("postcard-rescheduled", "改到周一一起画海边明信片。")
	in.Sender, in.SenderName = "lin", "小林"
	visit := observe("visiting-wedding", "今天参加同事家的喜酒，盼我儿子以后也有这一天。")
	statement := "发言者陈述：" + visit[0].Quote
	if _, e = m.memory.SaveMemory(t.Context(), companion.MemoryEntry{Scope: id, Subject: in.Sender, Kind: "episode", Detail: statement, Summary: statement, Sources: visit}, 0); e != nil {
		t.Fatal(e)
	}
	seedVideoReflection(t, m, in)
	seedVideoReflectionWith(t, m, in, "synthetic-interest-video", "这是我收集的蓝紫配色参考。", "synthetic-interest", "想找同样的蓝紫配色参考", "", true)
	seedVideoReflectionWith(t, m, in, "synthetic-feeling-video", "这是我录下的安静海面。", "synthetic-feeling", "", "当时有点平静", false)
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
