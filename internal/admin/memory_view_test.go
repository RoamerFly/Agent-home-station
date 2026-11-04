package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

type memorySummaryModel struct{}

func TestTextCorrectionCannotSilentlyEraseRelationship(t *testing.T) {
	f := setup(t)
	cfg := f.manager.Config().Companion
	cfg.Permissions.Remember = true
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "garden@chatroom", IsGroup: true, TraceID: "visit", Sender: "member", Text: "我爸来看花。"}
	scope := companion.ScopeID(in.Channel, in.Account, in.Thread)
	if err := f.manager.memory.Observe(in, cfg, time.Now()); err != nil {
		t.Fatal(err)
	}
	record, err := f.manager.memory.Read(scope, cfg, time.Now())
	if err != nil || len(record.Messages) != 1 {
		t.Fatalf("missing source: %v", err)
	}
	event, err := f.manager.memory.ReadEvent(t.Context(), []string{scope}, record.Messages[0].EventID)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := f.manager.memory.SaveMemory(t.Context(), companion.MemoryEntry{
		Scope: scope, Subject: in.Sender, Kind: "fact", Detail: in.Text,
		Sources: []companion.MemorySource{{EventID: event.ID, Quote: in.Text}},
		Assertions: []companion.MemoryAssertion{{
			Subject: in.Sender, Predicate: "father", Object: "relative:member:father", AssertedBy: in.Sender, State: "explicit",
			SubjectBasis: "speaker", SubjectText: "我", ObjectBasis: "relative", ObjectText: "爸", PredicateText: "爸",
			Sources: []companion.AssertionSource{{EventID: event.ID, EventRevision: event.Revision, Quote: in.Text}},
		}},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.login()
	code, _, _ := f.request("POST", "/api/memory/"+scope+"/entries/"+entry.ID,
		map[string]any{"action": "correct", "detail": "来看花", "expected_revision": entry.Version, "sources": entry.Sources}, true, true)
	got, err := f.manager.memory.ReadMemory(t.Context(), []string{scope}, entry.ID)
	if code != 400 || err != nil || got.Version != entry.Version || len(got.Assertions) != 1 {
		t.Fatalf("text edit changed a relationship: status=%d, version=%d, err=%v", code, got.Version, err)
	}
}

func TestMemorySourceReadChecksScopeAndOptionalRevision(t *testing.T) {
	f := setup(t)
	cfg := f.manager.Config().Companion
	cfg.Permissions.Remember = true
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "source@chatroom", IsGroup: true, TraceID: "source-message", Sender: "member", Text: "第一版来源"}
	if err := f.manager.memory.Observe(in, cfg, time.Now()); err != nil {
		t.Fatal(err)
	}
	scope := companion.ScopeID(in.Channel, in.Account, in.Thread)
	record, err := f.manager.memory.Read(scope, cfg, time.Now())
	if err != nil || len(record.Messages) != 1 {
		t.Fatal(err)
	}
	event, err := f.manager.memory.ReadEvent(t.Context(), []string{scope}, record.Messages[0].EventID)
	if err != nil || event.Revision < 1 {
		t.Fatal(err)
	}
	f.login()
	path := "/api/memory/" + scope + "/sources/" + event.ID
	if code, body, _ := f.request("GET", path, nil, false, false); code != 200 || !bytes.Contains(body, []byte("第一版来源")) {
		t.Fatalf("legacy source read: %d %s", code, body)
	}
	if code, _, _ := f.request("GET", path+"?revision="+fmt.Sprint(event.Revision), nil, false, false); code != 200 {
		t.Fatalf("current source revision rejected: %d", code)
	}
	for _, revision := range []string{"", "0", "abc", "1&revision=2"} {
		if code, _, _ := f.request("GET", path+"?revision="+revision, nil, false, false); code != 400 {
			t.Fatalf("invalid source revision %q: %d", revision, code)
		}
	}
	if code, body, _ := f.request("GET", path+"?revision="+fmt.Sprint(event.Revision+1), nil, false, false); code != 409 || bytes.Contains(body, []byte("第一版来源")) {
		t.Fatalf("mismatched source revision exposed body: %d %s", code, body)
	}
	otherScope := companion.ScopeID("wechat", "other-account", "source@chatroom")
	if code, _, _ := f.request("GET", "/api/memory/"+otherScope+"/sources/"+event.ID, nil, false, false); code != 404 {
		t.Fatalf("cross-account source accessible: %d", code)
	}
	if code, _, _ := f.request("GET", "/api/memory/"+scope+"/sources/missing-event?revision=1", nil, false, false); code != 404 {
		t.Fatalf("missing source admitted: %d", code)
	}
	if err := f.manager.memory.Clear(scope); err != nil {
		t.Fatal(err)
	}
	if code, body, _ := f.request("GET", path+"?revision="+fmt.Sprint(event.Revision), nil, false, false); code != 404 || bytes.Contains(body, []byte(in.Text)) {
		t.Fatalf("cleared source exposed: %d %s", code, body)
	}
}

func TestExperienceViewsRequireCurrentAccountAndKnownView(t *testing.T) {
	f := setup(t)
	cfg := f.manager.Config().Companion
	cfg.Permissions.Remember = true
	for _, account := range []string{"test", "other-account"} {
		in := companion.Input{Channel: "wechat", Account: account, Thread: "garden@chatroom", IsGroup: true, TraceID: "synthetic", Sender: "member", Text: "明天见。"}
		if err := f.manager.memory.Observe(in, cfg, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	path := func(account string) string {
		return "/api/companion/memory/" + companion.ScopeID("wechat", account, "garden@chatroom") + "/experiences"
	}
	for _, view := range []string{"relations", "open_commitments"} {
		if code, _, _ := f.request("GET", path("test")+"?view="+view, nil, false, false); code != 401 {
			t.Fatalf("anonymous %s: %d", view, code)
		}
	}
	f.login()
	if code, _, _ := f.request("GET", path("test")+"?view=unsupported", nil, false, false); code != 400 {
		t.Fatalf("unknown view returned %d", code)
	}
	for _, view := range []string{"relations", "open_commitments"} {
		query := "?view=" + view + "&person=member"
		if code, _, _ := f.request("GET", path("other-account")+query, nil, false, false); code != 404 {
			t.Fatalf("cross-account %s: %d", view, code)
		}
		code, body, _ := f.request("GET", path("test")+query, nil, false, false)
		var page companion.SharedExperiencePage
		if code != 200 || json.Unmarshal(body, &page) != nil || page.Items == nil || len(page.Items) != 0 {
			t.Fatalf("unbound remark appeared as %s: %d %s", view, code, body)
		}
	}
}

func (memorySummaryModel) Chat(context.Context, string, []map[string]string) (string, error) {
	return `{"version":1,"people":[],"topics":[],"current_focus":"synthetic summary","media":[],"pending_tasks":[],"preferences":[],"corrections":[]}`, nil
}

func TestMemoryHTTPFoldsRawHistoryUntilExplicitRead(t *testing.T) {
	f := setup(t)
	cfg := f.manager.Config().Companion
	cfg.Permissions.Remember = true
	cfg.MemoryMessages = 500
	cfg.Context = &companion.ContextSettings{AutoCompress: true, CompressAfter: 20, RecentMessages: 5, MaxInputChars: 8000}
	f.manager.config.Companion = cfg
	in := companion.Input{Channel: "wechat", Account: "test", Thread: "filehelper"}
	for i := 0; i < 125; i++ {
		in.TraceID, in.Text = fmt.Sprint(i), fmt.Sprintf("private-history-%03d", i)
		if err := f.manager.memory.Observe(in, cfg, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	engine, _ := companion.New(cfg, f.manager.memory, memorySummaryModel{}, "policy")
	if ok, err := engine.CompressContext(context.Background(), in, true); !ok || err != nil {
		t.Fatal(ok, err)
	}
	in.TraceID, in.Text = "new-one", "visible-new-turn"
	f.manager.memory.Observe(in, cfg, time.Now())
	id := companion.ScopeID("wechat", "test", "filehelper")
	f.login()
	code, body, _ := f.request("GET", "/api/companion/memory/"+id, nil, false, false)
	if code != 200 || bytes.Contains(body, []byte("private-history-")) || !bytes.Contains(body, []byte("visible-new-turn")) || !bytes.Contains(body, []byte("synthetic summary")) {
		t.Fatal("default view leaked folded history or lost summary", code)
	}
	code, body, _ = f.request("GET", "/api/companion/memory/"+id+"/history", nil, false, false)
	var page struct {
		Messages []companion.Turn `json:"messages"`
		Next     int              `json:"next_offset"`
		Revision string           `json:"revision"`
	}
	if code != 200 || json.Unmarshal(body, &page) != nil || len(page.Messages) != 100 || page.Messages[0].Text != "private-history-124" || page.Next != 100 {
		t.Fatal("history page missing or not newest first", code)
	}
	path := "/api/companion/memory/" + id + "/history?offset=100&revision=" + page.Revision
	code, body, _ = f.request("GET", path, nil, false, false)
	if code != 200 || !bytes.Contains(body, []byte("private-history-000")) || bytes.Contains(body, []byte("visible-new-turn")) {
		t.Fatal("history pagination crossed snapshot", code)
	}
	if code, _, _ := f.request("GET", strings.Replace(path, page.Revision, "stale", 1), nil, false, false); code != 409 {
		t.Fatal("stale history revision admitted", code)
	}
	if code, _, _ := f.request("GET", "/api/companion/memory/"+companion.ScopeID("wechat", "other", "filehelper")+"/history", nil, false, false); code != 404 {
		t.Fatal("other account history accessible", code)
	}
}
