package bot

import (
	"context"
	"errors"
	"testing"
	"wechat-bot/internal/companion"
)

func TestPrivateFailuresNeverCreateCannedChat(t *testing.T) {
	for _, failed := range []bool{false, true} {
		w, _, c, in := mediaWorker(t)
		in.Mode = "direct"
		var err, processingErr error
		if failed {
			err, processingErr = errors.New("synthetic model failure"), context.DeadlineExceeded
		}
		w.finishCharacter(context.Background(), c, in, companion.Decision{Action: "silent", Reason: "invalid_model_decision"}, err, processingErr, failed, failed)
		if len(w.runtime.Plans) != 0 || len(w.runtime.Deliveries) != 0 {
			t.Fatal("program generated chat text after a model failure", failed)
		}
	}
}

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

func TestPrivateModelFailureQueuesModelReportWithoutCannedReplyOrReportLoop(t *testing.T) {
	for _, reportOnly := range []bool{false, true} {
		w, _, c, in := mediaWorker(t)
		in.Mode, in.IsGroup, in.ReportOnly = "direct", false, reportOnly
		reports := 0
		w.config.ReportFailure = func(got companion.Input, reason string) error {
			reports++
			if got.Thread != in.Thread || reason != "model_generation_failed" {
				t.Fatal(got, reason)
			}
			return nil
		}
		w.finishCharacter(context.Background(), c, in, companion.Decision{}, errors.New("synthetic provider failure"), nil, true, true)
		want := 1
		if reportOnly {
			want = 0
		}
		if reports != want || len(w.runtime.Plans) != 0 || len(w.runtime.Deliveries) != 0 {
			t.Fatal("failed private run vanished, canned text sent, or recursive report", reports)
		}
	}
}
