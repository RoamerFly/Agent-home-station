package admin

import (
	"encoding/json"
	"math"
	"testing"
	"time"
	"wechat-bot/internal/billing"
	"wechat-bot/internal/deepseek"
)

func TestRoundUsageRecordsProviderAttemptsWithoutRebilling(t *testing.T) {
	f := setup(t)
	f.login()
	model := billing.ModelConfig{Provider: "deepseek", BaseURL: "https://api.deepseek.com", Model: "configured-model", APIKey: "synthetic-key"}
	actualModel := model
	actualModel.Model = "deepseek-v4-pro"
	if err := f.manager.billing.SetPrices(actualModel, &billing.Prices{Currency: "CNY", Input: 1, CacheHit: .5, Output: 2}); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	attempts := []deepseek.Usage{
		{Model: actualModel.Model, Purpose: "conversation", Time: stamp, Reported: true, Input: 1000, Output: 1000, CacheHit: 200},
		{Model: actualModel.Model, Purpose: "conversation", Time: stamp.Add(time.Second)},
	}
	for _, attempt := range attempts {
		if err := f.manager.billing.Record(model, attempt); err != nil {
			t.Fatal(err)
		}
		f.manager.recordModelUsage("room@chatroom", "round-1", model, attempt)
	}
	if got := f.manager.billing.Totals(); got.Requests != 2 || got.Reported != 1 || math.Abs(got.Costs["CNY"]-.0029) > 1e-10 {
		t.Fatalf("per-round activity changed aggregate billing: %+v", got)
	}
	copy, err := NewManager(f.dir, "", f.native)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	logs := copy.Logs()
	if len(logs) != 2 || logs[0].ThreadID != "room@chatroom" || logs[0].MessageID != "round-1" || !logs[0].UsageReported || !logs[0].UsagePriced || logs[0].InputTokens != 1000 || logs[0].CacheHitTokens != 200 || math.Abs(logs[0].EstimatedCosts["CNY"]-.0029) > 1e-10 {
		t.Fatalf("reported attempt was not persisted: %+v", logs)
	}
	if logs[1].UsageReported || logs[1].UsagePriced || logs[1].EstimatedCosts != nil {
		t.Fatalf("unreported attempt was priced as zero: %+v", logs[1])
	}
	code, data, _ := f.request("GET", "/api/logs", nil, false, false)
	if code != 200 {
		t.Fatalf("logs API status %d", code)
	}
	var response struct {
		Items []LogEntry `json:"items"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 2 || response.Items[0].ThreadID != "room@chatroom" || response.Items[0].MessageID != "round-1" || !response.Items[0].UsageReported || response.Items[0].EstimatedCosts["CNY"] == 0 || response.Items[1].UsageReported || response.Items[1].EstimatedCosts != nil {
		t.Fatalf("logs API lost provider usage: %+v", response.Items)
	}
}
