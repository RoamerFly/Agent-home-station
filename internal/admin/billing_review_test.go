package admin

import (
	"math"
	"testing"
	"time"

	"wechat-bot/internal/deepseek"
)

func TestReviewerUsageKeepsActualModelAndIndependentPrices(t *testing.T) {
	s, err := newBillingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := defaults()
	c.APIKey = "synthetic-review-usage-key"
	c.Model = "deepseek-flash"
	s.data.Prices[billingIdentity(c)] = tokenPrices{Currency: "CNY", Input: 99, CacheHit: 99, Output: 99}
	review := c
	review.Model = "deepseek-v4-pro"
	usage := deepseek.Usage{Model: review.Model, Purpose: "reply_grounding_check", Time: time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC), Reported: true, Input: 1000, CacheHit: 200, Output: 300}
	if err := s.record(c, usage); err != nil {
		t.Fatal(err)
	}
	entry := s.data.Models[billingIdentity(review)]
	prices, known := referencePrices(review, usage.Time)
	want := addUsage(tokenTotals{}, usage, prices, known)
	if entry.Model != review.Model || entry.Total.Input != 1000 || entry.Total.Output != 300 || math.Abs(entry.Total.Costs["CNY"]-want.Costs["CNY"]) > 1e-12 {
		t.Fatalf("review usage used the wrong model or price: %+v", entry)
	}
	if _, exists := s.data.Models[billingIdentity(c)]; exists || c.Model != "deepseek-flash" {
		t.Fatal("review usage changed the configured conversational identity")
	}
	usage.Model = "unpriced-review-model"
	if err := s.record(c, usage); err != nil {
		t.Fatal(err)
	}
	unpriced := c
	unpriced.Model = usage.Model
	if s.data.Models[billingIdentity(unpriced)].Total.Unpriced != 1 {
		t.Fatal("unknown review model inherited conversational prices")
	}
}
