package mcptools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestDiscoveryParsesRSSAtomAndBiliWithoutWatchingClaims(t *testing.T) {
	s := DiscoverySource{ID: "rss", Name: "RSS", Type: "rss", URL: "https://example.org/feed", Kind: "image", Enabled: true}
	for _, body := range []string{`<rss><channel><item><title>猫猫散步</title><link>https://example.org/cat</link><description><![CDATA[<p>在公园</p><img src="https://example.org/cat.jpg">]]></description></item></channel></rss>`, `<feed xmlns="http://www.w3.org/2005/Atom"><entry><title>猫猫散步</title><link rel="alternate" href="https://example.org/cat"/><content type="html">&lt;img src="https://example.org/cat.jpg"&gt;在公园</content></entry></feed>`} {
		items, e := parseDiscoveryFeed([]byte(body), s)
		if e != nil || len(items) != 1 || items[0].MediaURL != "https://example.org/cat.jpg" || items[0].Kind != "image" {
			t.Fatal(items, e)
		}
	}
	s = DefaultDiscoverySources()[0]
	items, e := parseDiscoveryBili([]byte(`{"code":0,"data":{"list":[{"bvid":"BV1425hzpEtn","title":"猫猫","duration":90,"desc":"日常","pubdate":100}]}}`), s)
	if e != nil || len(items) != 1 || items[0].DurationMS != 90000 || !IsPublicVideoPage(items[0].URL) {
		t.Fatal(items, e)
	}
	if _, e = parseDiscoveryBili([]byte(`{"code":-412,"data":null}`), s); e == nil {
		t.Fatal("denied source claimed success")
	}
}
func TestDiscoverySourcesValidateAndUnrelatedShellsAreNotIntercepted(t *testing.T) {
	for _, s := range DefaultDiscoverySources() {
		if e := ValidateDiscoverySource(s); e != nil {
			t.Fatal(s, e)
		}
	}
	s := DefaultDiscoverySources()[0]
	s.URL = "https://example.org/arbitrary-api"
	if ValidateDiscoverySource(s) == nil {
		t.Fatal("arbitrary adapter API")
	}
	if _, handled, _ := discoveryListPage(context.Background(), "https://example.org/v/popular/all"); handled {
		t.Fatal("arbitrary host intercepted")
	}
}
func TestLiveDiscoveryPublicSources(t *testing.T) {
	workspace := os.Getenv("WECHAT_LIVE_WORKSPACE")
	if workspace == "" || os.Getenv("WECHAT_DISCOVERY_LIVE") != "1" {
		t.Skip("explicit read-only public feed diagnostics")
	}
	reports := make([]map[string]any, len(DefaultDiscoverySources()))
	var wg sync.WaitGroup
	for i, s := range DefaultDiscoverySources() {
		wg.Add(1)
		go func(i int, s DiscoverySource) {
			defer wg.Done()
			items, err := DiscoverPublic(context.Background(), s, "")
			row := map[string]any{"source": s.Name, "kind": s.Kind, "count": len(items), "success": err == nil}
			if err != nil {
				row["reason"] = FailureDescription(err)
			} else if len(items) > 0 {
				row["sample"] = items[0]
			}
			reports[i] = row
		}(i, s)
	}
	wg.Wait()
	b, _ := json.MarshalIndent(reports, "", "  ")
	if e := os.WriteFile(filepath.Join(workspace, "research/evidence/discovery-public-sources.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
	good := 0
	for _, r := range reports {
		t.Log(r["source"], r["count"], r["reason"])
		if r["success"] == true {
			good++
		}
	}
	if good < 2 {
		t.Fatal("fewer than two sources available")
	}
}

func TestLiveReportedSurfListAndRelatedRead(t *testing.T) {
	workspace := os.Getenv("WECHAT_LIVE_WORKSPACE")
	if workspace == "" || os.Getenv("WECHAT_DISCOVERY_LIVE") != "1" {
		t.Skip("explicit reported public list/related diagnostic")
	}
	ctx := context.Background()
	text, err := webFetch(ctx, json.RawMessage(`{"url":"https://www.bilibili.com/v/popular/all"}`))
	report := map[string]any{"list_success": err == nil, "list_is_metadata_not_watched": false, "related_success": false, "related_count": 0, "wechat_sends": 0}
	var list struct {
		Metadata   bool            `json:"metadata_only"`
		Candidates []DiscoveryItem `json:"candidates"`
	}
	_ = json.Unmarshal([]byte(text), &list)
	report["list_is_metadata_not_watched"] = list.Metadata
	if err != nil {
		report["list_reason"] = FailureDescription(err)
	} else if len(list.Candidates) > 0 {
		items, e := DiscoverPublic(ctx, DefaultDiscoverySources()[0], list.Candidates[0].URL)
		report["related_success"] = e == nil
		report["related_count"] = len(items)
		if e != nil {
			report["related_reason"] = FailureDescription(e)
		}
	}
	b, _ := json.MarshalIndent(report, "", "  ")
	if e := os.WriteFile(filepath.Join(workspace, "research/evidence/discovery-reported-list-related.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
	if err != nil || !list.Metadata || len(list.Candidates) == 0 {
		t.Fatal("reported list still cannot be read", err)
	}
}
