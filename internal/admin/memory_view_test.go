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
