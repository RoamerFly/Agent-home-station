package admin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

func TestDiscoveryPersistsDiverseCandidatesAndContinuesAfterOneSourceFails(t *testing.T) {
	f := setup(t)
	p, _, _ := f.manager.toolAccess()
	p.SetTrace(companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "surf-one", Surf: true})
	p.discoveryReader = func(_ context.Context, s mcptools.DiscoverySource, _ string) ([]mcptools.DiscoveryItem, error) {
		if s.Kind == "voice" {
			return nil, &mcptools.WebError{Code: "network_unavailable"}
		}
		return []mcptools.DiscoveryItem{{Title: s.Name + "猫咪玩耍", URL: "https://example.org/" + s.ID, Kind: s.Kind, Source: s.Name}}, nil
	}
	f.manager.surf.Settings.Discovery.MaxSources = 4
	result, err := p.Call(context.Background(), "web_discover", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Candidates []mcptools.DiscoveryItem `json:"candidates"`
		Failures   []string                 `json:"source_failures"`
	}
	if json.Unmarshal([]byte(result), &data) != nil || len(data.Candidates) < 1 || len(data.Failures) < 1 {
		t.Fatal(result)
	}
	reloaded, err := newSurfHistory(f.dir)
	if err != nil || len(reloaded.data.Sessions) != 1 || len(reloaded.data.Items) == 0 {
		t.Fatal("history lost", err)
	}
	// Same candidate identities are not reintroduced on a second page in the session.
	result, err = p.Call(context.Background(), "web_discover", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(result), &data)
	ids := map[string]bool{}
	for _, x := range reloaded.data.Items {
		ids[x.Item.ID] = true
	}
	for _, x := range data.Candidates {
		if ids[x.ID] {
			t.Fatal("seen candidate repeated", x)
		}
	}
	if _, err = p.discover(context.Background(), json.RawMessage(`{"related_url":"https://www.bilibili.com/video/BV1425hzpEtn/"}`)); err == nil {
		t.Fatal("unseen related source admitted")
	}
}
func TestSurfTopicCooldownBookmarkAndActualAcceptedSharePersist(t *testing.T) {
	f := setup(t)
	p, _, _ := f.manager.toolAccess()
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper", TraceID: "surf-a", Surf: true}
	p.SetTrace(in)
	x := mcptools.DiscoveryItem{ID: "one", Title: "三比特纠缠有三种", Summary: "三比特纠缠研究", URL: "https://a.example.org/one", Kind: "text"}
	s := f.manager.surfHistory
	s.data.Items = []surfSeen{{Item: x, Seen: time.Now(), Shared: map[string]time.Time{}}}
	s.data.Sessions = []surfSession{{ID: in.TraceID, Thread: in.Thread, Candidates: []mcptools.DiscoveryItem{x}, Visited: []string{x.URL}}}
	d, err := p.ReviewSurf(context.Background(), companion.Decision{Action: "silent", SurfAction: "bookmark", SurfItemID: "one"})
	if err != nil || d.Action != "silent" || !s.data.Items[0].Bookmarked {
		t.Fatal(d, err)
	}
	d, err = p.ReviewSurf(context.Background(), companion.Decision{Action: "reply", SurfItemID: "one", Messages: []string{"这个新发现分享给你"}})
	if err != nil || d.Action != "reply" || s.data.Sessions[0].State != "prepared" {
		t.Fatal(d, err)
	}
	if len(s.data.Items[0].Shared) != 0 {
		t.Fatal("preparation treated as delivery")
	}
	f.manager.record(bot.Record{Event: "message_sent", Status: "accepted", MessageID: in.TraceID, ThreadID: in.Thread})
	reloaded, err := newSurfHistory(f.dir)
	if err != nil || reloaded.data.Items[0].Shared[in.Thread].IsZero() {
		t.Fatal("accepted share lost", err)
	}
	y := mcptools.DiscoveryItem{ID: "two", Title: "新论文：三比特纠缠有三种，补全数学拼图", URL: "https://b.example.org/different"}
	s.data.Sessions = append(s.data.Sessions, surfSession{ID: "surf-b", Thread: in.Thread, Candidates: []mcptools.DiscoveryItem{y}, Visited: []string{y.URL}})
	in.TraceID = "surf-b"
	p.SetTrace(in)
	d, err = p.ReviewSurf(context.Background(), companion.Decision{Action: "reply", SurfItemID: "two", Messages: []string{"另一家报道"}})
	if err != nil || d.Reason != "surf_duplicate_topic" {
		t.Fatal("cross-url repeated topic accepted", d, err)
	}
	// Unrelated nature content is not blocked simply because it is also science.
	if sameSurfTopic("猫咪第一次看见雪", "鲸鱼迁徙的季节到了") {
		t.Fatal("novel topic overfiltered")
	}
}
func TestDiscoveryValidationAndHistoryAuth(t *testing.T) {
	f := setup(t)
	if code, _, _ := f.request("GET", "/api/surf/history", nil, false, false); code != 401 {
		t.Fatal(code)
	}
	f.login()
	if code, _, _ := f.request("GET", "/api/surf/history", nil, true, false); code != 200 {
		t.Fatal(code)
	}
	d := defaultDiscovery()
	d.Weights = map[string]int{"recommendations": 3, "community": 1}
	if err := validateDiscovery(d); err != nil {
		t.Fatal("non-100 weights rejected", err)
	}
	d.Weights["community"] = -1
	if validateDiscovery(d) == nil {
		t.Fatal("negative weight admitted")
	}
	d.Weights = map[string]int{"search": 0}
	if validateDiscovery(d) == nil {
		t.Fatal("zero total admitted")
	}
	if _, err := newSurfHistory("\x00"); err == nil || errors.Is(err, context.Canceled) {
		t.Fatal("bad history path silently reset")
	}
}

func TestReportedQuantumCaptionsAreDuplicateWithoutBlockingNewTopics(t *testing.T) {
	a := "刚冲浪看到个挺好玩的东西：量子期刊 Quantum 上有篇论文讲“三比特纠缠有三种”，把以前缺的那块数学拼图补上了 🔬 群里有对量子感兴趣的可以聊聊～ https://quantum-journal.org/"
	b := "刚随手翻到一篇量子期刊上的论文，标题叫《三比特纠缠的三种》，说是补上了纠缠理论里缺的一块数学拼图，看着还挺有意思的 🔬 https://different.example.org/"
	if !sameSurfTopic(a, b) {
		t.Fatal("reported paraphrases not recognized")
	}
	if sameSurfTopic(a, "可爱小猫在雪地里撒欢的日常") {
		t.Fatal("unrelated new topic blocked")
	}
}

func TestImportOldAcceptedSurfSharesPreservesAgeAndDoesNotImportPending(t *testing.T) {
	s, err := newSurfHistory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-3 * time.Hour).UTC()
	logs := []LogEntry{{Event: "message_sent", Status: "accepted", ThreadID: "room@chatroom", MessageID: "surf-old", Time: stamp.Format(time.RFC3339Nano), Content: "三比特纠缠有三种 https://quantum-journal.org/"}, {Event: "message_sent", Status: "unknown", ThreadID: "room@chatroom", MessageID: "surf-unknown", Time: stamp.Format(time.RFC3339Nano), Content: "另一个 https://example.org/unknown"}}
	if err = s.importAccepted(logs); err != nil {
		t.Fatal(err)
	}
	if err = s.importAccepted(logs); err != nil {
		t.Fatal(err)
	}
	if len(s.data.Items) != 1 || !s.data.Items[0].Shared["room@chatroom"].Equal(stamp) || len(s.data.Sessions) != 0 {
		t.Fatal("migration duplicated, refreshed age, or created a new browsing task", s.data)
	}
}

func TestSurfNoCandidatesExplainsNetworkFailureAndSearchFallbackCanJoinSession(t *testing.T) {
	f := setup(t)
	p, _, _ := f.manager.toolAccess()
	in := companion.Input{Thread: "filehelper", TraceID: "surf-empty", Surf: true}
	p.SetTrace(in)
	s := f.manager.surfHistory
	s.data.Sessions = []surfSession{{ID: in.TraceID, Thread: in.Thread, Candidates: []mcptools.DiscoveryItem{}, Failures: []string{"网络不可用"}}}
	d, err := p.ReviewSurf(context.Background(), companion.Decision{Action: "silent"})
	if err != nil || d.Reason != "surf_sources_unavailable" || s.data.Sessions[0].State != "failed" {
		t.Fatal(d, err)
	}
	extra, err := p.rememberSearchCandidates("Search provider: Test\n\n1. 公开猫咪视频\nhttps://www.bilibili.com/video/BV1425hzpEtn/\n真实简介\n")
	if err != nil || len(s.data.Sessions[0].Candidates) != 1 || extra == "" {
		t.Fatal("successful fallback search not retained", extra, err)
	}
}
