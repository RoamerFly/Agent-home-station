package admin

import (
	"errors"
	"strings"
	"wechat-bot/internal/companion"
)

// Only fixed codes enter the activity journal, never raw provider/path errors.
func botFailureReason(err error) string {
	if errors.Is(err, companion.ErrDeliveryMemoryConflict) {
		return "delivery_memory_conflict"
	}
	message := err.Error()
	if strings.Contains(message, "native") {
		return "bot_event_read_failed"
	}
	if strings.Contains(message, "another bot") {
		return "bot_state_already_owned"
	}
	if strings.Contains(message, "checkpoint") || strings.Contains(message, "persistent") || strings.Contains(message, "queue") {
		return "bot_saved_state_unavailable"
	}
	return "bot_state_processing_failed"
}
