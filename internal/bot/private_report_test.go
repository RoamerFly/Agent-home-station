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

func TestModelFailureRetainsLocalDiagnosticWithoutPrivateReport(t *testing.T) {
	w, _, c, in := mediaWorker(t)
	in.Mode = "direct"
	records := []Record{}
	w.config.Record = func(r Record) { records = append(records, r) }
	w.finishCharacter(context.Background(), c, in, companion.Decision{}, errors.New("synthetic provider diagnostic"), nil, false, false)
	failed := 0
	reportedFailure := false
	for _, r := range records {
		if r.Event == "model_failed" && r.Reason == "synthetic provider diagnostic" {
			failed++
		}
		if r.Event == "character_decision" && r.Reason == "operation_reported_privately" {
			reportedFailure = r.Status == "failed" && r.Category == "error"
		}
	}
	if failed != 1 || reportedFailure || len(w.runtime.Deliveries) != 0 {
		t.Fatal("private status report erased model diagnostic or sent canned reply", failed, w.runtime.Deliveries)
	}
}
