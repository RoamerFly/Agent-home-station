package bot

import (
	"context"
	"strings"
	"time"
	"wechat-bot/internal/companion"
)

func (w *Worker) reviewUncertainDelivery(ctx context.Context, c *companion.Engine, d *delivery) {
	sent := []string{}
	for _, m := range w.runtime.Media {
		if m.Input.Thread == d.Thread && m.State == "accepted" && (m.Trace == d.Trace || d.Input.OriginTraceID != "" && m.Input.OriginTraceID == d.Input.OriginTraceID) {
			sent = append(sent, "[已由微信接受的"+m.Kind+"] "+m.Title+"，不得重复发送")
		}
	}
	for _, other := range w.runtime.Deliveries {
		if other != d && other.Thread == d.Thread && other.Trace == d.Trace && other.State == "accepted" {
			sent = append(sent, other.Text)
		}
	}
	reviewCtx, cancel := context.WithTimeout(w.modelContext(ctx, d.Thread, d.Trace), 30*time.Second)
	review, err := c.ReviewDelivery(reviewCtx, d.Input, sent, d.Text, companion.DeliveryReview{Reason: d.Reason, Attempts: d.Attempts, Created: d.Created})
	cancel()
	d.Next = time.Now().Add(deliveryBackoff(w.config.Options.RetryDelaySeconds, d.UncertainRounds))
	reason := "delivery_review_unavailable"
	if err == nil {
		switch review.Action {
		case "continue":
			d.RetryReviewReady = true
			d.Next = time.Now()
			reason = "delivery_model_continue"
		case "replace":
			text := strings.TrimSpace(strings.Join(review.Messages, "\n"))
			if len(text) > 0 && len(text) <= 4096 {
				if d.SubmittedText == "" {
					d.SubmittedText = d.Text
				}
				d.Text = text
				d.RetryReviewReady = true
				d.Next = time.Now()
				reason = "delivery_model_replace"
			}
		case "delay":
			if next := time.Now().Add(time.Duration(review.DelaySeconds) * time.Second); next.After(d.Next) {
				d.Next = next
			}
			d.Checks = 0
			reason = "delivery_model_delay"
		case "cancel":
			if review.Reason != "" {
				d.State = "stopped"
				d.Reason = "delivery_model_cancel"
				reason = d.Reason
			}
		}
	}
	w.persistErr = w.saveRuntime()
	w.activity(Record{Event: "delivery_review", Category: "decision", ThreadID: d.Thread, MessageID: d.Trace, Content: d.Text, Reason: reason, Status: d.State, RetryAttempts: d.Attempts, RetryAt: d.Next.Format(time.RFC3339)})
	if d.State == "stopped" {
		w.activity(Record{Event: "delivery_stopped", Category: "decision", ThreadID: d.Thread, MessageID: d.Trace, Reason: d.Reason, Status: "cancelled"})
	}
}
