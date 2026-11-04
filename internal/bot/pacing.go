package bot

import (
	"crypto/rand"
	"math/big"
	"strings"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func (w *Worker) addressed(m linuxnative.Message) bool {
	if m.ReplyToSelf {
		return true
	}
	if m.Mentions.State == "known" && m.Mentions.Self != nil && *m.Mentions.Self {
		return true
	}
	text := strings.ToLower(m.Body)
	question := strings.ContainsAny(text, "?？")
	for _, q := range []string{"什么", "怎么", "能不能", "可以", "吗", "呢", "帮我", "请问"} {
		question = question || strings.Contains(text, q)
	}
	if !question {
		return false
	}
	names := []string{"蓝色大肥鱼", "deepseek", "大肥鱼"}
	if c, ok := w.model.(*companion.Engine); ok {
		names = append(names, strings.ToLower(c.Config.Name))
	}
	for _, name := range names {
		if name != "" && strings.Contains(text, name) {
			return true
		}
	}
	return false
}
func (w *Worker) maxMessageAge() time.Duration {
	if w.recovering && w.config.Options != nil && w.config.Options.RecoverBacklog {
		return time.Duration(w.config.Options.BacklogHours) * time.Hour
	}
	return w.config.MaxAge
}

func (w *Worker) messageInTime(m linuxnative.Message, now time.Time) bool {
	created, err := time.Parse(time.RFC3339, m.CreatedAt)
	if err != nil || created.After(now.Add(30*time.Second)) {
		return false
	}
	age, limit := now.Sub(created), w.maxMessageAge()
	if age <= limit {
		return true
	}
	// Exclude only the deliberate online collection wait. An old message on
	// arrival, or one that sits unhandled long after the deadline, still expires.
	if w.runtime != nil && strings.HasSuffix(m.ThreadID, "@chatroom") {
		for i := range w.runtime.Inbox {
			item := &w.runtime.Inbox[i]
			if !item.Backlog && !item.CollectedAt.IsZero() && item.Event.Data.MessageID == m.ID && item.Event.Data.ThreadID == m.ThreadID {
				end := item.AggregateUntil
				if now.Before(end) {
					end = now
				}
				return end.Sub(item.CollectedAt) >= age-limit
			}
		}
	}
	return false
}
func (w *Worker) replyDelay(c *companion.Engine) time.Duration {
	if w.config.Options == nil || !w.config.Options.RandomDelay {
		if c != nil {
			return c.Delay()
		}
		return 0
	}
	o := w.config.Options
	value := o.DelayMinSeconds
	spread := o.DelayMaxSeconds - value
	if spread > 0 {
		if n, e := rand.Int(rand.Reader, big.NewInt(int64(spread+1))); e == nil {
			value += int(n.Int64())
		}
	}
	return time.Duration(value) * time.Second
}
