package bot

import (
	"context"
	"testing"
	"time"
	"wechat-bot/internal/companion"
)

func TestCancelledTaskStopsQueuedMediaWithoutSubmittingNextAttachment(t *testing.T) {
	w, n, c, in := mediaWorker(t)
	in.TaskID = "plugin-task"
	w.runtime.Media = []*mediaDelivery{{Key: "pending", Trace: in.TraceID, Input: in, SourceID: "source", Kind: "voice", State: "prepared", Created: time.Now()}}
	w.config.AgentTaskActive = func(input companion.Input) bool { return input.TaskID != "plugin-task" }
	w.reconcileMedia(context.Background())
	w.runtime.Media[0].Next = time.Time{}
	w.reconcileMedia(context.Background())
	if n.sends != 0 || w.runtime.Media[0].State != "stopped" || !w.runtime.Media[0].UserCancelled {
		t.Fatal(n.sends, w.runtime.Media[0])
	}
	c.Config.Permissions.ReplyText = true
	w.finishCharacter(context.Background(), c, in, companion.Decision{Action: "reply", Messages: []string{"迟到说明"}}, nil, nil, false, false)
	if n.sends != 0 || n.fakeSender.calls != 0 {
		t.Fatal("late output sent after task cancellation")
	}
}
