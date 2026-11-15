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

// Keep the fixed public failure code while retaining a bounded, sanitized
// detail in the private admin log for local recovery. This is especially
// useful on Windows where checkpoint replacement errors can be transient.
func botFailureDetail(err error) string {
	if err == nil {
		return "unknown"
	}
	value := strings.ToLower(err.Error())
	value = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, value)
	value = strings.Trim(value, "_")
	if len(value) > 160 {
		value = value[:160]
	}
	if value == "" {
		return "unknown"
	}
	return value
}
