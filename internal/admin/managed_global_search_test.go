package admin

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func globalSearchFixture(t *testing.T) (*webFixture, *permittedTools) {
	t.Helper()
	f, p := trustedAgent(t)
	c := f.manager.agent.config()
	c.CrossConversation = true
	c.PrivilegedThreads = []string{"*"}
	if err := f.manager.agent.save(c); err != nil {
		t.Fatal(err)
	}
	f.manager.config.Companion.Permissions.Remember = true
	f.manager.config.GroupRules = append(f.manager.config.GroupRules, GroupRule{ID: "source@chatroom", Name: "合成来源群", Mode: "mention"})
	return f, p
}

func TestGlobalSearchUsesOriginalTimesInsteadOfCacheUpdates(t *testing.T) {
	f, p := globalSearchFixture(t)
	now := time.Now().UTC()
	old := now.Add(-2 * time.Hour).In(time.FixedZone("UTC+8", 8*3600))
	newer := now.Add(-time.Hour)
	for _, pair := range []struct {
		id string
		at time.Time
	}{{"old-original", old}, {"new-original", newer}} {
		in := companion.Input{Channel: "wechat", Account: "test", Thread: "source@chatroom", Sender: "member", SenderName: "合成成员", TraceID: pair.id, MessageTime: pair.at.Format(time.RFC3339Nano), Text: "fixture 文件消息"}
		if err := f.manager.memory.Observe(in, f.manager.config.Companion, now); err != nil {
			t.Fatal(err)
		}
	}
	request := assetRequest{TraceID: "old-original", RequesterID: "member", MessageTime: old.Format(time.RFC3339Nano), At: old.UTC()}
	if err := f.manager.saveAssets([]mediaAsset{{ID: "asset-old", Account: "test", Scope: companion.ScopeID("wechat", "test", "source@chatroom"), Filename: "fixture-old.txt", Updated: now, FirstRequest: &request}}); err != nil {
		t.Fatal(err)
	}
	result, err := p.Call(context.Background(), "agent_global_search", json.RawMessage(`{"query":"fixture"}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Items []struct {
			Kind            string
			At              time.Time
			CachedUpdatedAt time.Time `json:"cached_updated_at"`
			ID              string    `json:"message_id"`
			Sender          string    `json:"sender_name"`
			Basis           string    `json:"time_basis"`
		}
	}
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 3 || got.Items[0].ID != "new-original" {
		t.Fatalf("latest message displaced by old cache/timezone: %s", result)
	}
	var sawAsset bool
	for _, item := range got.Items {
		if item.Kind == "asset" {
			sawAsset = true
			if !item.At.Equal(old) || !item.CachedUpdatedAt.Equal(now) || item.ID != "old-original" || item.Basis != "message" {
				t.Fatalf("lost original asset provenance: %s", result)
			}
		} else if item.Sender != "合成成员" {
			t.Fatalf("lost original sender name: %s", result)
		}
	}
	if !sawAsset {
		t.Fatal("missing asset")
	}
}

func TestGlobalSearchTimeWindowAndCancellation(t *testing.T) {
	f, p := globalSearchFixture(t)
	now := time.Now().UTC()
	since, before := now.Add(-2*time.Hour), now.Add(-time.Hour)
	for i, at := range []time.Time{since.Add(-time.Nanosecond), since, before.Add(-time.Nanosecond), before} {
		in := companion.Input{Channel: "wechat", Account: "test", Thread: "source@chatroom", Sender: "member", TraceID: string(rune('a' + i)), Text: "fixture", MessageTime: at.Format(time.RFC3339Nano)}
		if err := f.manager.memory.Observe(in, f.manager.config.Companion, now); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(map[string]any{"since": since.Format(time.RFC3339Nano), "before": before.Format(time.RFC3339Nano), "limit": 1})
	result, err := p.Call(context.Background(), "agent_global_search", raw)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Total, NextOffset int
		HasMore           bool `json:"has_more"`
		Items             []struct {
			ID string `json:"message_id"`
		}
	}
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 2 || !got.HasMore || len(got.Items) != 1 || got.Items[0].ID != "c" {
		t.Fatalf("time window/pagination incorrect: %s", result)
	}
	for _, raw := range []string{`{"since":"yesterday"}`, `{"before":"2026-10-29"}`, `{"since":"2026-10-29T01:00:00Z","before":"2026-10-29T00:00:00Z"}`} {
		if _, err := p.Call(context.Background(), "agent_global_search", json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid time range accepted: %s", raw)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.callProject(ctx, "agent_global_search", json.RawMessage(`{}`)); err != context.Canceled {
		t.Fatalf("search ignored cancellation: %v", err)
	}
}

func TestGlobalSearchConsoleForwardsTimeBounds(t *testing.T) {
	f, _ := globalSearchFixture(t)
	f.login()
	params := url.Values{"since": {time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)}, "before": {time.Now().UTC().Format(time.RFC3339Nano)}}
	status, data, _ := f.request("GET", "/api/agent/global-search?"+params.Encode(), nil, false, false)
	var result struct {
		Window map[string]string `json:"time_window"`
	}
	if status != 200 || json.Unmarshal(data, &result) != nil || result.Window["since_inclusive"] != params.Get("since") || result.Window["before_exclusive"] != params.Get("before") {
		t.Fatalf("console dropped time bounds: %d %s", status, data)
	}
	status, _, _ = f.request("GET", "/api/agent/global-search?since=yesterday", nil, false, false)
	if status != 400 {
		t.Fatalf("invalid console time accepted: %d", status)
	}
}

func TestGlobalSearchLabelsLegacyAssetTime(t *testing.T) {
	now := time.Now().UTC()
	at, basis, request := assetOriginTime(mediaAsset{Updated: now})
	if !at.Equal(now) || basis != "cache_updated" || request != nil {
		t.Fatal("legacy cache time presented as message time")
	}
	first := assetRequest{TraceID: "legacy-message", MessageTime: "invalid", At: now.Add(-time.Hour)}
	at, basis, request = assetOriginTime(mediaAsset{Updated: now, Requests: []assetRequest{first}})
	if !at.Equal(first.At) || basis != "request_recorded" || request.TraceID != first.TraceID {
		t.Fatal("legacy request time/provenance lost")
	}
}
