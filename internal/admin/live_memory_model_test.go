package admin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

// Opt-in billed model test. Reads provider settings only; all conversation,
// memory, tools and output belong to a disposable synthetic account.
func TestLiveMemoryExtractionSynthetic(t *testing.T) {
	root := os.Getenv("WECHAT_LIVE_MEDIA_ROOT")
	if root == "" || os.Getenv("WECHAT_MEMORY_MODEL_LIVE") != "1" {
		t.Skip("opt-in billed synthetic extraction test")
	}
	private, e := loadConfig(filepath.Join(root, "admin"))
	if e != nil {
		t.Fatal("provider configuration unavailable")
	}
	f := setup(t)
	provider, e := f.manager.makeModel(private)
	if e != nil {
		t.Fatal("provider unavailable")
	}
	cfg := companion.Defaults()
	cfg.Enabled = true
	cfg.Permissions.Remember = true
	store, e := companion.NewStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	engine, e := companion.New(cfg, store, provider, "")
	if e != nil {
		t.Fatal(e)
	}
	in := companion.Input{Channel: "wechat", Account: "synthetic-memory", Thread: "synthetic-room", Sender: "alice"}
	id := companion.ScopeID(in.Channel, in.Account, in.Thread)
	lines := []string{"我叫小林，2016年去过苏州。", "我喜欢喝红茶，但不喝咖啡。", "我不喝咖啡是口味选择，不是过敏。", "周末计划读完《海边笔记》，现在还没读完。", "上次谈话还没讨论完书里的灯塔。", "下次继续聊灯塔，不要把计划写成已经完成。", "我偏好简短直接的中文回答。", "我喜欢无糖饮料。", "我家猫咪的名字是团子。", "不要把团子写成狗。", "我以前住过成都。", "现在住在杭州。"}
	for i, text := range lines {
		in.TraceID = fmt.Sprint("source-", i)
		in.Text = text
		if e = store.Observe(in, cfg, time.Now()); e != nil {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	defer cancel()
	if e = engine.MaintainScope(ctx, id, true); e != nil {
		t.Fatal("synthetic extraction failed", e)
	}
	page, e := store.SearchMemory(ctx, companion.MemoryQuery{Scopes: []string{id}, Query: "咖啡", Limit: 5})
	if e != nil || len(page.Items) == 0 {
		t.Fatal("synthetic negation was not retrievable", e)
	}
	found := false
	for _, hit := range page.Items {
		m := hit.Entry
		for _, src := range m.Sources {
			ev, e := store.ReadEvent(ctx, []string{id}, src.EventID)
			if e != nil || !strings.Contains(ev.Text, src.Quote) {
				t.Fatal("invalid live model citation")
			}
		}
		if strings.Contains(m.Detail, "不") && strings.Contains(m.Detail, "咖啡") {
			found = true
		}
	}
	if !found {
		t.Fatal("negation lost")
	}
	status, e := store.MemoryStatus(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("real_provider_synthetic_extraction=true negation_source_verified=true entries=%v no_real_conversation_reads=true real_wechat_sends=0", status["entries"])
}
