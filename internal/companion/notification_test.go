package companion

import (
	"context"
	"testing"
)

func TestScheduledNotificationUsesSavedText(t *testing.T) {
	e := &Engine{Config: Config{Enabled: true}}
	d, err := e.Respond(context.Background(), Input{Mode: "direct", NotificationText: "提醒你：喝水"})
	if err != nil || d.Action != "reply" || len(d.Messages) != 1 || d.Messages[0] != "提醒你：喝水" {
		t.Fatal("notification changed", d, err)
	}
}
