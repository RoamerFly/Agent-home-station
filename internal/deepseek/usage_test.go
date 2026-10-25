package deepseek

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUsageIncludesBilledInvalidAnswerAndUnknownAttempts(t *testing.T) {
	responses := []string{
		`{"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_cache_hit_tokens":60,"prompt_tokens_details":{"cached_tokens":60}},"choices":[{"message":{"content":"partial"},"finish_reason":"length"}]}`,
		`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`,
	}
	n := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, responses[n]); n++ }))
	defer server.Close()
	observed := []Usage{}
	c := &Client{endpoint: server.URL, http: server.Client(), onUsage: func(u Usage) { observed = append(observed, u) }}
	if _, err := c.requestOnce(context.Background(), []byte(`{}`)); err == nil {
		t.Fatal("incomplete answer accepted")
	}
	if _, err := c.requestOnce(context.Background(), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if len(observed) != 2 || !observed[0].Reported || observed[0].Input != 100 || observed[0].CacheHit != 60 || observed[0].Output != 20 || observed[1].Reported {
		t.Fatal(observed)
	}
	for _, body := range []string{`{"usage":{"prompt_tokens":-1,"completion_tokens":1}}`, `{"usage":{"prompt_tokens":2,"completion_tokens":1,"prompt_cache_hit_tokens":3}}`, `{"usage":{"prompt_tokens":2}}`} {
		if parseUsage([]byte(body), time.Now()).Reported {
			t.Fatal("invalid usage counted", body)
		}
	}
	u := parseUsage([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":8}}}`), time.Now())
	if !u.Reported || u.CacheHit != 8 {
		t.Fatal(u)
	}
}

type balanceTransport func(*http.Request) (*http.Response, error)

func (f balanceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestBalanceOfficialOriginOnlyAndSafeFailures(t *testing.T) {
	calls := 0
	body := `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"12.34","granted_balance":"2.00","topped_up_balance":"10.34"}]}`
	c := &Client{endpoint: "https://api.deepseek.com/v1/chat/completions", key: "test-balance-key", http: &http.Client{Transport: balanceTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://api.deepseek.com/user/balance" || r.Header.Get("Authorization") != "Bearer test-balance-key" {
			t.Fatal("wrong credential destination")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
	b, err := c.Balance(context.Background())
	if err != nil || len(b.Infos) != 1 || b.Infos[0].Total != "12.34" {
		t.Fatal(b, err)
	}
	for _, raw := range []string{"https://api.deepseek.com.evil.invalid/chat/completions", "https://other.example/v1/chat/completions", "https://api.deepseek.com/other/chat/completions"} {
		c.endpoint = raw
		if _, err = c.Balance(context.Background()); err == nil {
			t.Fatal("unverified balance endpoint used")
		}
	}
	if calls != 1 {
		t.Fatal("key sent to unverified origin")
	}
	c.endpoint = "https://api.deepseek.com/chat/completions"
	body = strings.Replace(body, "12.34", "NaN", 1)
	if _, err = c.Balance(context.Background()); err == nil {
		t.Fatal("NaN balance accepted")
	}
}

func TestOfficialSearchSendsOnlyQueryAndNeverForwardsKeyToCompatibleHost(t *testing.T) {
	calls := 0
	c := &Client{endpoint: "https://api.deepseek.com/v1/chat/completions", key: "test-key", http: &http.Client{Transport: balanceTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://api.deepseek.com/v1/tools/search" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatal("wrong search credential destination")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 3 || body["query"] != "Traveling Light" {
			t.Fatal("private context or wrong search arguments", body)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"results":[{"title":"Traveling Light","url":"https://example.org/song","summary":"An audio source"}]}`)), Header: make(http.Header)}, nil
	})}}
	items, err := c.Search(context.Background(), "Traveling Light")
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	c.endpoint = "https://compatible.example/v1/chat/completions"
	if _, err = c.Search(context.Background(), "query"); err == nil || calls != 1 {
		t.Fatal("official search key sent to an unverified origin")
	}
}
