package bot

import (
	"context"
	"testing"
	"wechat-bot/internal/companion"
)

func TestOperationalFallbackIsReportedPrivatelyInsteadOfGroupReply(t *testing.T) {
	w, _, c, in := mediaWorker(t)
	in.IsGroup = true
	in.Mode = "direct"
	in.Thread = "source@chatroom"
	w.allowed[in.Thread] = true
	reports := 0
	w.config.ReportFailure = func(got companion.Input, reason string) error {
		reports++
		if got.Thread != in.Thread || reason != "decision_failure_reported" {
			t.Fatal(got, reason)
		}
		return nil
	}
	w.finishCharacter(context.Background(), c, in, companion.Decision{Action: "silent", Reason: "tool_budget_exhausted"}, nil, nil, false, false)
	if reports != 1 || len(w.runtime.Plans) != 0 || len(w.runtime.Deliveries) != 0 {
		t.Fatal("technical fallback leaked to group", reports, w.runtime.Plans, w.runtime.Deliveries)
	}
}
