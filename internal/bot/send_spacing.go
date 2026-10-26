package bot

import (
	"time"
	"wechat-bot/internal/companion"
)

// Text, progress, retries and media share this durable conversation gate.
func (w *Worker) sendGate(thread string) time.Time {
	if w.runtime.SendGates == nil {
		w.runtime.SendGates = map[string]time.Time{}
	}
	return w.runtime.SendGates[thread]
}
func (w *Worker) advanceSendGate(thread string) {
	c, _ := w.model.(*companion.Engine)
	gap := w.replyDelay(c)
	if gap < time.Second {
		gap = time.Second
	}
	next := time.Now().Add(gap)
	if next.After(w.sendGate(thread)) {
		w.runtime.SendGates[thread] = next
	}
}
