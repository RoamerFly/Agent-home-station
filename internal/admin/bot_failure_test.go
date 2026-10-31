package admin

import (
	"errors"
	"fmt"
	"testing"
	"wechat-bot/internal/companion"
)

func TestBotFailureReasonDoesNotExposeRawErrors(t *testing.T) {
	for _, test := range []struct {
		err  error
		code string
	}{{fmt.Errorf("wrapped: %w", companion.ErrDeliveryMemoryConflict), "delivery_memory_conflict"}, {errors.New("native read unavailable"), "bot_event_read_failed"}, {errors.New("another bot owns this state directory"), "bot_state_already_owned"}, {errors.New("persistent queue corrupt"), "bot_saved_state_unavailable"}, {errors.New("private filesystem path and credential"), "bot_state_processing_failed"}} {
		if got := botFailureReason(test.err); got != test.code {
			t.Fatal(got, test.code)
		}
	}
}
