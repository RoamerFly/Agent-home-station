package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

type extractionFixture func(context.Context, string, []map[string]string) (string, error)

func (f extractionFixture) Chat(c context.Context, s string, m []map[string]string) (string, error) {
	return f(c, s, m)
}

func sourceEntry(t *testing.T, s *Store, c Config, in Input, at time.Time, kind, detail string, year int) MemoryEntry {
	t.Helper()
	if err := s.Observe(in, c, at); err != nil {
		t.Fatal(err)
	}
	id := ScopeID(in.Channel, in.Account, in.Thread)
	eid := eventID(id, Turn{Role: "user", TraceID: in.TraceID}, 0)
	m, err := s.SaveMemory(t.Context(), MemoryEntry{Scope: id, Subject: in.Sender, Kind: kind, Detail: detail, EventYear: year, Sources: []MemorySource{{eid, in.Text}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// Suite v2 uses 100 labelled queries over 20 source-backed narratives. It
// includes negation, corrections, old dates, identity collisions and paraphrase
// vocabulary. It measures deterministic lexical retrieval, not LLM extraction.
func TestMemoryQualityV2HundredQueries(t *testing.T) {
	s, c, in := memoryFixture(t)
	id := ScopeID(in.Channel, in.Account, in.Thread)
	topics := []string{"红茶", "咖啡", "杭州", "成都", "火锅", "牛奶", "游泳", "骑车", "书法", "钢琴", "猫咪", "狗狗", "樱花", "雪山", "电影", "小说", "周末", "早餐", "晚饭", "散步"}
	hits, total := 0, 0
	for i, topic := range topics {
		in.Sender = fmt.Sprintf("person-%d", i)
		in.TraceID = fmt.Sprintf("v2-old-%d", i)
		in.Text = fmt.Sprintf("2016年我和同名的小林在青岛聊过%s。", topic)
		old := sourceEntry(t, s, c, in, time.Now().Add(-800*24*time.Hour), "episode", in.Text, 2016)
		// Identical names and source text in another account must stay inaccessible.
		other := in
		other.Account = "other-account"
		sourceEntry(t, s, c, other, time.Now(), "episode", other.Text, 2016)
		in.TraceID = fmt.Sprintf("v2-new-%d", i)
		in.Text = fmt.Sprintf("更正%s：2016年是在苏州，不是青岛。约好下次继续聊，尚未办完。", topic)
		cur := sourceEntry(t, s, c, in, time.Now().Add(-time.Hour), "episode", in.Text, 2016)
		cur.Supersedes = old.ID
		var err error
		cur, err = s.SaveMemory(t.Context(), cur, cur.Version)
		if err != nil {
			t.Fatal(err)
		}
		// Five different intents share real vocabulary with their labelled source.
		for _, q := range []MemoryQuery{
			{Query: topic + " 是青岛还是苏州"},
			{Query: topic + " 更正"},
			{Query: topic + " 下次未完事项"},
			{Query: topic + " 以前的约定", EventYear: 2016},
			{Query: topic + " 小林的往事", Subject: in.Sender},
		} {
			q.Scopes = []string{id}
			q.Limit = 5
			p, err := s.SearchMemory(t.Context(), q)
			if err != nil {
				t.Fatal(err)
			}
			total++
			found := false
			for _, h := range p.Items {
				if h.Entry.Scope != id {
					t.Fatal("scope leak")
				}
				if h.Entry.ID == old.ID {
					t.Fatal("superseded entry exposed")
				}
				if h.Entry.ID == cur.ID {
					found = true
				}
			}
			if found {
				hits++
			}
		}
	}
	if total != 100 || hits < 90 {
		t.Fatalf("v2 recall@5=%d/%d", hits, total)
	}
	t.Logf("suite=memory-quality-v2 lexical_source_recall@5=%d/%d scope_leaks=0 superseded_current=0", hits, total)
}

func TestMemoryIncrementalBatchRestartBudgetAndInvalidSource(t *testing.T) {
	s, c, in := memoryFixture(t)
	id := ScopeID(in.Channel, in.Account, in.Thread)
	calls := 0
	invalid := false
	model := extractionFixture(func(ctx context.Context, policy string, m []map[string]string) (string, error) {
		calls++
		var payload struct {
			Sources []MemoryEvent `json:"sources"`
		}
		if err := json.Unmarshal([]byte(m[0]["content"]), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Sources) != 12 {
			t.Fatalf("batch size %d", len(payload.Sources))
		}
		ev := payload.Sources[0]
		quote := ev.Text
		if invalid {
			quote = "not-in-source"
		}
		out := map[string]any{"entries": []MemoryEntry{{Subject: ev.Sender, Kind: "preference", Detail: ev.Text, LastMention: time.Now().Add(1000 * time.Hour), Sources: []MemorySource{{ev.ID, quote}}}}}
		b, _ := json.Marshal(out)
		return string(b), nil
	})
	engine, err := New(c, s, model, "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 11; i++ {
		in.TraceID = fmt.Sprint(i)
		in.Text = "我不喝含糖饮料。"
		if err = s.Observe(in, c, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err = engine.MaintainScope(t.Context(), id, false); err != nil || calls != 0 {
		t.Fatal("premature extraction", err, calls)
	}
	in.TraceID = "11"
	s.Observe(in, c, time.Now())
	invalid = true
	if err = engine.MaintainScope(t.Context(), id, true); err == nil {
		t.Fatal("invalid citation accepted")
	}
	var processed int
	s.db.QueryRow("SELECT processed_seq FROM jobs WHERE scope=?", id).Scan(&processed)
	if processed != 0 {
		t.Fatal("failed output advanced cursor")
	}
	invalid = false
	if err = engine.MaintainScope(t.Context(), id, true); err != nil {
		t.Fatal(err)
	}
	p, err := s.SearchMemory(t.Context(), MemoryQuery{Scopes: []string{id}})
	if err != nil || len(p.Items) != 1 {
		t.Fatal(p, err)
	}
	if time.Until(p.Items[0].Entry.LastMention) > time.Second {
		t.Fatal("model refreshed mention time")
	}
	settings := MemoryDefaults()
	settings.DailyTokens = 1
	engine.Config.Memory = &settings
	in.TraceID = "next"
	s.Observe(in, c, time.Now().Add(-10*time.Minute))
	before := calls
	if err = engine.MaintainScope(t.Context(), id, false); err != nil || calls != before {
		t.Fatal("budget did not defer", err)
	}
	dir := strings.TrimSuffix(s.path, "memory.sqlite")
	s.Close()
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	pending, err := s2.MaintenancePending(t.Context(), id)
	if err != nil || !pending {
		t.Fatal("restart lost pending progress", err)
	}
}

func TestMemoryTimeBoundariesRevisionAndForgetting(t *testing.T) {
	s, c, in := memoryFixture(t)
	id := ScopeID(in.Channel, in.Account, in.Thread)
	at := time.Date(2026, 10, 29, 1, 2, 3, 0, time.UTC)
	in.Text = "2020年在苏州，2020-04-05见面。"
	m := sourceEntry(t, s, c, in, at, "episode", in.Text, 2020)
	for _, q := range []MemoryQuery{{Since: at.Format(time.RFC3339Nano), Before: at.Add(time.Nanosecond).Format(time.RFC3339Nano)}, {EventYear: 2020}} {
		q.Scopes = []string{id}
		p, e := s.SearchMemory(t.Context(), q)
		if e != nil || len(p.Items) != 1 {
			t.Fatal(p, e)
		}
	}
	m.EventTime = "2020-04-05T00:00:00Z"
	m, e := s.SaveMemory(t.Context(), m, m.Version)
	if e != nil {
		t.Fatal(e)
	}
	p, e := s.SearchMemory(t.Context(), MemoryQuery{Scopes: []string{id}, Since: "2020-01-01T00:00:00Z", Before: "2021-01-01T00:00:00Z", TimeBasis: "event"})
	if e != nil || len(p.Items) != 1 {
		t.Fatal(p, e)
	}
	s.SaveEmbedding(t.Context(), m, "fake", []float32{1, 0})
	m.Status = "deleted"
	if _, e = s.SaveMemory(t.Context(), m, m.Version); e != nil {
		t.Fatal(e)
	}
	if got := s.Context(in, c, time.Now()); len(got.Messages) != 0 || got.Summary != "" {
		t.Fatal("forgotten source survived recent context")
	}
	for _, raw := range []bool{false, true} {
		p, e = s.SearchMemory(t.Context(), MemoryQuery{Scopes: []string{id}, Originals: raw, Space: "fake", Vector: []float32{1, 0}})
		if e != nil || len(p.Items) != 0 {
			t.Fatal("forgotten source recalled", p, e)
		}
	}
	m.ID = ""
	m.Status = "active"
	if _, e = s.SaveMemory(t.Context(), m, 0); e == nil {
		t.Fatal("reextraction resurrected forgotten source")
	}
}

func TestMemoryContextReductionAndCurrentRequest(t *testing.T) {
	s, c, in := memoryFixture(t)
	model := extractionFixture(func(context.Context, string, []map[string]string) (string, error) { return `{"entries":[]}`, nil })
	engine, e := New(c, s, model, "")
	if e != nil {
		t.Fatal(e)
	}
	var baseline, selected int
	for scenario := 0; scenario < 5; scenario++ {
		scope := Scope{ID: ScopeID(in.Channel, in.Account, in.Thread)}
		for i := 0; i < 60; i++ {
			scope.Messages = append(scope.Messages, Turn{Role: "user", TraceID: fmt.Sprint(i), Sender: "alice", Text: strings.Repeat("这里是已经讨论过的合成背景资料，周末计划阅读和散步。", 8+scenario), Time: time.Now().Add(time.Duration(i-60) * time.Minute)})
		}
		oldSettings := MemoryDefaults()
		oldSettings.RecentTokens = 100000
		engine.Config.Memory = &oldSettings
		old := engine.contextTurns(scope, in)
		current := MemoryDefaults()
		engine.Config.Memory = &current
		now := engine.contextTurns(scope, in)
		for _, v := range old {
			baseline += EstimateTokens(v["content"])
		}
		for _, v := range now {
			selected += EstimateTokens(v["content"])
		}
	}
	reduction := 1 - float64(selected)/float64(baseline)
	t.Logf("suite=context-v2 scenarios=5 baseline_estimated=%d selected_estimated=%d reduction=%.2f%% method=same_text_estimator", baseline, selected, 100*reduction)
	if reduction < .2 {
		t.Fatal("context reduction below 20%")
	}
	if e := ValidateContextWindow("protocol", []map[string]string{{"content": strings.Repeat("当前请求", 1000)}}, []byte(`[{"tool":"memory_read"}]`), 100, 20); e == nil {
		t.Fatal("oversized required request silently accepted")
	}
}

func TestMemoryOriginalRevisionInvalidatesFact(t *testing.T) {
	s, c, in := memoryFixture(t)
	in.Text = "我喜欢红茶"
	m := sourceEntry(t, s, c, in, time.Now(), "preference", in.Text, 0)
	in.Text = "更正：我只喝白水"
	if e := s.Observe(in, c, time.Now()); e != nil {
		t.Fatal(e)
	}
	ev, e := s.ReadEvent(t.Context(), []string{m.Scope}, m.Sources[0].EventID)
	if e != nil || ev.Revision != 2 || ev.Text != in.Text {
		t.Fatal("source revision not retained", ev, e)
	}
	fact, e := s.ReadMemory(t.Context(), []string{m.Scope}, m.ID)
	if e != nil || fact.Status != "conflicted" {
		t.Fatal("edited source left determined fact", fact, e)
	}
}

func BenchmarkMemorySingleScopeWrite(b *testing.B) {
	s, e := NewStore(b.TempDir())
	if e != nil {
		b.Fatal(e)
	}
	defer s.Close()
	c := Defaults()
	c.Permissions.Remember = true
	in := Input{Channel: "wechat", Account: "synthetic", Thread: "fixed", Sender: "alice", Text: "合成消息：今天继续读书，稍后整理笔记。"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		in.TraceID = fmt.Sprint(i)
		if e = s.Observe(in, c, time.Now()); e != nil {
			b.Fatal(e)
		}
	}
}

func TestMemoryWriteVolumeSynthetic(t *testing.T) {
	if os.Getenv("WECHAT_MEMORY_BENCH") != "1" {
		t.Skip("opt-in write volume experiment")
	}
	s, c, in := memoryFixture(t)
	s.db.Exec("PRAGMA wal_autocheckpoint=0")
	s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	for i := 0; i < 100; i++ {
		in.TraceID = fmt.Sprint(i)
		in.Text = "合成消息：今天继续读书，稍后整理笔记。"
		if e := s.Observe(in, c, time.Now()); e != nil {
			t.Fatal(e)
		}
	}
	info, e := os.Stat(s.path + "-wal")
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("suite=write-volume-v1 events=100 wal_bytes=%d wal_bytes_per_event=%d checkpoint=disabled_for_volume_measurement_only", info.Size(), info.Size()/100)
}

func TestMemoryRefreshExcludesForgottenAndPreservesCurrent(t *testing.T) {
	s, c, in := memoryFixture(t)
	in.Text = "我喜欢红茶"
	m := sourceEntry(t, s, c, in, time.Now(), "preference", in.Text, 0)
	engine, _ := New(c, s, extractionFixture(func(context.Context, string, []map[string]string) (string, error) { return "", nil }), "")
	dynamic := engine.memoryContext(t.Context(), in)
	current := `{"kind":"current_message","text":"当前请求不可截断"}`
	turns := []map[string]string{{"role": "user", "content": dynamic}, {"role": "user", "content": current}}
	m.Status = "deleted"
	if _, e := s.SaveMemory(t.Context(), m, m.Version); e != nil {
		t.Fatal(e)
	}
	result := engine.refreshMemoryPackets(t.Context(), turns)
	if result[len(result)-1]["content"] != current {
		t.Fatal("current request changed")
	}
	if strings.Contains(result[0]["content"], "我喜欢红茶") {
		t.Fatal("forgotten entry frozen in running context")
	}
}

func TestEmbeddingAdapterProtocolAndBudget(t *testing.T) {
	calls := 0
	bad := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer synthetic" {
			t.Error("protocol/auth")
		}
		if bad {
			fmt.Fprint(w, `{"data":[{"index":0,"embedding":[0,0]}]}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,0]}],"usage":{"total_tokens":10}}`)
	}))
	defer server.Close()
	cfg := EmbeddingConfig{BaseURL: server.URL + "/v1", Model: "fake", Dimensions: 2, APIKey: "synthetic"}
	r, e := cfg.Embed(t.Context(), []string{"synthetic"})
	if e != nil || !r.Reported || r.Tokens != 10 {
		t.Fatal(r, e)
	}
	bad = true
	if _, e = cfg.Embed(t.Context(), []string{"synthetic"}); e == nil {
		t.Fatal("zero vector accepted")
	}
	bad = false
	s, c, in := memoryFixture(t)
	in.Text = "我喜欢红茶"
	m := sourceEntry(t, s, c, in, time.Now(), "preference", in.Text, 0)
	s.RecordMaintenanceUsage(t.Context(), "extraction", 100, true)
	before := calls
	n, e := s.RebuildEmbeddingsBudget(t.Context(), m.Scope, cfg, 32, 100)
	if e != nil || n != 0 || calls != before {
		t.Fatal("automatic embedding ignored budget", n, e)
	}
	n, e = s.RebuildEmbeddings(t.Context(), m.Scope, cfg, 32)
	if e != nil || n != 1 {
		t.Fatal("explicit embedding truncated", n, e)
	}
	cfg.Revision = "new"
	n, e = s.RebuildEmbeddings(t.Context(), m.Scope, cfg, 32)
	if e != nil || n != 1 {
		t.Fatal("new space not rebuilt", n, e)
	}
	p, e := s.SearchMemory(t.Context(), MemoryQuery{Scopes: []string{m.Scope}, Query: "unrelated-paraphrase", Space: r.Space, Vector: []float32{1, 0}})
	if e != nil || len(p.Items) != 0 {
		t.Fatal("different embedding spaces mixed", p, e)
	}
}

// Opt-in repeatable latency experiment; fixtures and logs contain no real data.
func TestMemoryPerformanceAcceptance(t *testing.T) {
	if os.Getenv("WECHAT_MEMORY_BENCH") != "1" {
		t.Skip("opt-in latency experiment")
	}
	results := []time.Duration{}
	for _, count := range []int{10, 100} {
		s, c, in := memoryFixture(t)
		for i := 0; i < count; i++ {
			in.Thread = fmt.Sprint("room-", i)
			for j := 0; j < 40; j++ {
				in.TraceID = fmt.Sprint(j)
				in.Text = "合成原话：喜欢无糖茶，准备周末继续读书。"
				if e := s.Observe(in, c, time.Now()); e != nil {
					t.Fatal(e)
				}
			}
		}
		in.Thread = "fixed-room"
		lat := []time.Duration{}
		for i := 0; i < 120; i++ {
			in.TraceID = fmt.Sprint(i)
			start := time.Now()
			if e := s.Observe(in, c, time.Now()); e != nil {
				t.Fatal(e)
			}
			if i >= 20 {
				lat = append(lat, time.Since(start))
			}
		}
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		p95 := lat[94]
		results = append(results, p95)
		t.Logf("env=%s/%s %s scopes=%d unrelated_events=%d write_p50=%s write_p95=%s", runtime.GOOS, runtime.GOARCH, runtime.Version(), count, count*40, lat[49], p95)
	}
	ratio := float64(results[1]) / float64(results[0])
	t.Logf("10x unrelated scopes p95_ratio=%.3f", ratio)
	if ratio > 2 {
		t.Fatalf("write scaling p95 ratio %.3f >2", ratio)
	}
}
