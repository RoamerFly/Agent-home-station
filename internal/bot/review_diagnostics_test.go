package bot

import (
	"context"
	"testing"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/deepseek"
)

func TestStoppedReviewRecordsFailureWithoutSendingOrSocialSilence(t *testing.T) {
	w, _, engine, in := mediaWorker(t)
	in.IsGroup = true
	var records []Record
	w.config.Record = func(r Record) { records = append(records, r) }
	failure := &companion.ReviewFailure{Code: "review_units_missing", Stage: "review_protocol", ReviewState: "initial_check_failed", Detail: "复核响应缺少逐句核验结果"}
	w.finishCharacter(context.Background(), engine, in, companion.Decision{Action: "silent", Reason: "grounding_check_failed", ReviewFailure: failure}, nil, nil, false, false)
	for _, r := range records {
		if r.Event == "character_decision" {
			if r.Category != "error" || r.Status != "failed" || r.ReviewFailure == nil || r.ReviewFailure.Code != "review_units_missing" {
				t.Fatalf("diagnostic lost: %+v", r)
			}
			if len(w.runtime.Plans) != 0 || len(w.runtime.Deliveries) != 0 {
				t.Fatal("failed review created a send plan")
			}
			return
		}
	}
	t.Fatal("no review failure activity")
}

func TestGroundingResponseMetadataSurvivesActivityProjection(t *testing.T) {
	r := modelRecord("room", "synthetic", deepseek.Diagnostics{Code: "model_review_protocol_invalid", Stage: "review_protocol", HTTPStatus: 200, Attempts: 1, MaxAttempts: 2, FinishReason: "stop", ContentBytes: 123, JSONValid: true, FormatAttempt: 2, MaxFormatAttempts: 2, ProtocolCode: "review_units_missing"})
	if r.ModelHTTPStatus != 200 || r.ModelFinishReason != "stop" || r.ModelContentBytes != 123 || !r.ModelJSONValid || r.ModelFormatAttempt != 2 || r.ModelMaxFormatAttempts != 2 || r.ModelProtocolCode != "review_units_missing" {
		t.Fatalf("metadata lost: %+v", r)
	}
}
