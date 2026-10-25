package admin

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
	"wechat-bot/internal/deepseek"
)

func TestUsageLedgerCachesCostOnceAndPreservesOldPriceAfterRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := newBillingStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := defaults()
	c.APIKey = "private-key-not-in-ledger"
	u := deepseek.Usage{Time: time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC), Reported: true, Input: 100, Output: 20, CacheHit: 60}
	if err = s.record(c, u); err != nil {
		t.Fatal(err)
	}
	want := (40*.15 + 60*.003 + 20*.6) / 1_000_000
	if got := s.data.Total.Costs["USD"]; math.Abs(got-want) > 1e-12 {
		t.Fatal("cache tokens charged twice", got, want)
	}
	s.data.Prices[billingIdentity(c)] = tokenPrices{Currency: "CNY", Input: 2, CacheHit: 1, Output: 3}
	if err = s.record(c, u); err != nil {
		t.Fatal(err)
	}
	if err = s.record(c, deepseek.Usage{Time: u.Time}); err != nil {
		t.Fatal(err)
	}
	loaded, err := newBillingStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	total := loaded.data.Total
	if total.Input != 200 || total.Output != 40 || total.CacheHit != 120 || total.Requests != 3 || total.Reported != 2 || math.Abs(total.Costs["USD"]-want) > 1e-12 || total.Costs["CNY"] <= 0 {
		t.Fatal(total)
	}
	b, _ := os.ReadFile(loaded.path)
	if bytes.Contains(b, []byte(c.APIKey)) {
		t.Fatal("key leaked to usage ledger")
	}
	c.APIKey = "different-account-key"
	if billingIdentity(c) == billingIdentity(defaults()) {
		t.Fatal("identities not isolated")
	}
}
func TestBillingAPIAuthPriceConflictAndUnsupportedBalance(t *testing.T) {
	f := setup(t)
	if code, _, _ := f.request("GET", "/api/billing", nil, false, false); code != 401 {
		t.Fatal(code)
	}
	f.login()
	code, b, _ := f.request("GET", "/api/billing", nil, false, false)
	if code != 200 {
		t.Fatal(code)
	}
	var summary struct {
		ActiveID string `json:"active_id"`
	}
	_ = json.Unmarshal(b, &summary)
	input := map[string]any{"active_id": summary.ActiveID, "currency": "CNY", "input_per_million": 1, "cache_hit_per_million": .1, "output_per_million": 2}
	if code, _, _ := f.request("POST", "/api/billing/prices", input, true, false); code != 403 {
		t.Fatal("no CSRF", code)
	}
	if code, _, _ := f.request("POST", "/api/billing/prices", input, true, true); code != 200 {
		t.Fatal(code)
	}
	input["active_id"] = "previous-provider"
	if code, _, _ := f.request("POST", "/api/billing/prices", input, true, true); code != 409 {
		t.Fatal("stale pricing overwrote another provider", code)
	}
	f.manager.config.Provider = "openai-compatible"
	f.manager.config.BaseURL = "https://example.org/v1"
	if code, b, _ := f.request("POST", "/api/billing/balance", map[string]any{}, true, true); code != 200 || !bytes.Contains(b, []byte("unsupported")) {
		t.Fatal(code, string(b))
	}
}
func TestReferencePricePeakAndWeekendAndUnknownModel(t *testing.T) {
	c := defaults()
	weekday := time.Date(2026, 10, 26, 2, 0, 0, 0, time.UTC)
	p, ok := referencePrices(c, weekday)
	weekend, _ := referencePrices(c, weekday.AddDate(0, 0, -1))
	if !ok || p.Input != 2*weekend.Input {
		t.Fatal(p, weekend)
	}
	c.Model = "unverified-model"
	if _, ok = referencePrices(c, weekday); ok {
		t.Fatal("unknown model assigned a guessed price")
	}
}

func TestBillingScriptServedWithExecutableMIME(t *testing.T) {
	f := setup(t)
	code, _, response := f.request("GET", "/assets/billing.js", nil, false, false)
	if code != 200 || response.Header.Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Fatal("browser would refuse billing script", code, response.Header.Get("Content-Type"))
	}
}
