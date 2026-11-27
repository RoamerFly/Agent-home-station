package bot

import (
	"context"
	"strings"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

// Receipt routing lives in the durable inbox. No periodic wake is scheduled:
// a decision is possible only when a real incoming receipt owns the batch.
const (
	mentionTrigger     = "trigger"
	mentionFollowup    = "followup"
	mentionReview      = "review"
	mentionCollection  = 2 * time.Second
	mentionObservation = 30 * time.Second
)

type mentionReceipt struct {
	Kind          string    `json:"kind"`
	Sender        string    `json:"sender,omitempty"`
	Parent        string    `json:"parent,omitempty"`
	ClosedAt      time.Time `json:"closed_at,omitzero"`
	OccurredAt    time.Time `json:"occurred_at,omitzero"`
	ObservingFrom time.Time `json:"observing_from,omitzero"`
}

func (w *Worker) mentionRecallGeneration(thread string) int {
	if c, ok := w.model.(*companion.Engine); ok && c.Store != nil && w.mentionRoom(thread) {
		return c.Store.NativeRecallGeneration(w.source.Account(), thread)
	}
	return 0
}
func (w *Worker) mentionRoom(thread string) bool {
	r := w.config.GroupRules[thread]
	return strings.HasSuffix(thread, "@chatroom") && (r.Mode == "" || r.Mode == "mention")
}

func (w *Worker) mentionItem(thread, id string) *inboxItem {
	if w.runtime != nil {
		for i := len(w.runtime.Inbox) - 1; i >= 0; i-- {
			item := &w.runtime.Inbox[i]
			if item.Event.Data.MessageID == id && item.Event.Data.ThreadID == thread {
				return item
			}
		}
	}
	return nil
}

func (w *Worker) mentionTurnAllowed(m linuxnative.Message) bool {
	if !w.mentionRoom(m.ThreadID) {
		return false
	}
	item := w.mentionItem(m.ThreadID, m.ID)
	return item != nil && (item.OwnerModelReview || item.Mention != nil && (item.Mention.Kind == mentionReview || item.Mention.Kind == mentionFollowup))
}

func (w *Worker) mentionTaskInterruption(m linuxnative.Message) bool {
	return w.hasExplicitCorrectionTarget(m) || w.runtime != nil && parseVoiceInterrupt(m.Body).stop && len(w.voiceCancellationCandidates(m)) > 0
}

func (w *Worker) startMentionObservation(r Record) {
	if !w.mentionRoom(r.ThreadID) || !((r.Event == "message_sent" && r.Status == "accepted" && r.Origin == "bot") || (r.Event == "character_decision" && (r.Status == "silent" || r.Status == "wait"))) {
		return
	}
	item := w.mentionItem(r.ThreadID, r.MessageID)
	if item == nil || item.Mention == nil || !item.Mention.ObservingFrom.IsZero() || (item.Mention.Kind == mentionReview && r.Event == "character_decision") {
		return
	}
	if r.Event == "message_sent" {
		for _, plan := range w.runtime.Plans {
			if plan.Input.Thread == r.ThreadID && plan.Input.TraceID == r.MessageID && !plan.terminal() {
				return
			}
		}
		for _, media := range w.runtime.Media {
			if media.Input.Thread == r.ThreadID && media.Trace == r.MessageID && !media.terminal() {
				return
			}
		}
	}
	item.Mention.ObservingFrom = time.Now().UTC()
	w.persistErr = w.saveRuntime()
}

func (w *Worker) prepareMentionReceipts(ctx context.Context) error {
	for i := range w.runtime.Inbox {
		item := &w.runtime.Inbox[i]
		thread := item.Event.Data.ThreadID
		if !w.mentionRoom(thread) || item.Event.Data.Direction != "incoming" || item.Mention != nil || item.OwnerModelReview || item.ToolStarted || item.Phase != "received" && item.Phase != "read_pending" || time.Now().Before(item.RetryAfter) {
			continue
		}
		if w.draining() || ctx.Err() != nil {
			return ctx.Err()
		}
		if item.ReceivedAt.IsZero() {
			item.ReceivedAt = item.Updated
		}
		if w.config.AuthorizeThread != nil {
			check, cancel := context.WithTimeout(ctx, 10*time.Second)
			allowed, err := w.config.AuthorizeThread(check, thread)
			cancel()
			if err != nil {
				w.queuePhase(item.Event.Data.MessageID, "read_pending")
				continue
			}
			w.allowed[thread] = allowed
		}
		if !w.allowed[thread] {
			w.queuePhase(item.Event.Data.MessageID, "skipped")
			continue
		}
		read, cancel := context.WithTimeout(ctx, 10*time.Second)
		m, err := w.source.Message(read, item.Event.Data.MessageID)
		cancel()
		if err != nil {
			w.queuePhase(item.Event.Data.MessageID, "read_pending")
			continue
		}
		if m.ID != item.Event.Data.MessageID || m.ThreadID != thread || m.Direction != "incoming" || m.AccountID != w.source.Account() || m.IsPassiveNotice() {
			w.queuePhase(item.Event.Data.MessageID, "skipped")
			continue
		}
		if m.IsQuotedText() {
			w.readQuoteContext(ctx, &m)
		}
		allowed, err := w.archiveMentionReceipt(ctx, m, item.Event, item.RecallGeneration)
		if err != nil {
			return err
		}
		if !allowed {
			w.queuePhase(m.ID, "skipped")
			continue
		}
		// The reply epoch controls activation, not local receipt/history.
		// A message queued before a mode change still belongs in compression,
		// while it must never turn into a new request after that change.
		if !w.authorizedMessage(m) {
			w.queuePhase(m.ID, "skipped")
			continue
		}
		w.routeMentionReceipt(item, m)
		if err := w.saveRuntime(); err != nil {
			return err
		}
	}
	return w.persistErr
}

// Store.Observe keeps native text and attachment metadata in the local
// transcript for later compression. It does not invoke the model or a reader.
func (w *Worker) archiveMentionReceipt(ctx context.Context, m linuxnative.Message, e linuxnative.Event, generation int) (bool, error) {
	c, ok := w.model.(*companion.Engine)
	if !ok || !c.Config.Enabled || c.Store == nil {
		return true, nil
	}
	current := c.Store.NativeRecallGeneration(m.AccountID, m.ThreadID)
	in := companion.Input{Channel: w.config.Channel, Account: w.source.Account(), Thread: m.ThreadID, Sender: m.SenderID, MessageTime: m.CreatedAt, TraceID: m.ID, Text: messageSummary(m)}
	original := ""
	if m.Type == 1 || m.IsQuotedText() {
		original = m.Body
	}
	in.OriginalText = &original
	bindInputSource(&in, m, c)
	in.Source.Sequence = sourceSequence(e)
	allowed, err := c.Store.NativeRecallAllowed(ctx, in)
	if err != nil || !allowed || current != generation || !w.allowed[m.ThreadID] {
		return false, err
	}
	if !c.Config.Permissions.Remember {
		return true, nil
	}
	return true, c.Store.Observe(in, c.Config, time.Now())
}

func (w *Worker) routeMentionReceipt(item *inboxItem, m linuxnative.Message) {
	now := time.Now().UTC()
	arrived := item.ReceivedAt
	if arrived.IsZero() {
		arrived = item.Updated
	} // queue records from older versions
	if arrived.IsZero() {
		arrived = now
	}
	stamp, err := time.Parse(time.RFC3339Nano, m.CreatedAt)
	if err != nil {
		item.Phase = "skipped"
		return
	}
	var collection, observation *inboxItem
	for i := len(w.runtime.Inbox) - 1; i >= 0; i-- {
		prior := &w.runtime.Inbox[i]
		if prior == item || prior.Event.Data.ThreadID != m.ThreadID || prior.Mention == nil || w.roundClosed(m.ThreadID, prior.Event.Data.MessageID) {
			continue
		}
		if collection == nil && prior.Phase == "received" && prior.Mention.Kind != mentionReview && prior.Mention.Sender == m.SenderID && m.SenderID != "" && arrived.Before(prior.AggregateUntil) && !arrived.Before(prior.CollectedAt) && !stamp.Before(prior.Mention.OccurredAt) {
			collection = prior
		}
		start := prior.Mention.ObservingFrom
		// WeChat timestamps may have one-second precision. Receipt time supplies
		// the other boundary so a late or pre-observation backlog cannot reopen it.
		if !start.IsZero() && (prior.Mention.ClosedAt.IsZero() || arrived.Before(prior.Mention.ClosedAt)) && !arrived.Before(start) && arrived.Before(start.Add(mentionObservation)) && !stamp.Before(start.Truncate(time.Second)) && stamp.Before(start.Add(mentionObservation)) &&
			(observation == nil || start.After(observation.Mention.ObservingFrom)) {
			observation = prior
		}
	}
	if collection != nil && (verifiedGroupAddressed(m) || (m.Quote == nil && len(m.Mentions.IDs) == 0)) && !item.Backlog && !collection.Backlog {
		item.BacklogParent, item.Phase = collection.Event.Data.MessageID, "backlog_grouped"
		return
	}
	kind, parent := mentionTrigger, ""
	deadline := arrived.Add(mentionCollection)
	if !verifiedGroupAddressed(m) {
		switch {
		case w.mentionTaskInterruption(m):
			kind, deadline = mentionFollowup, arrived
		case observation != nil && !w.quietUnmentionedGroupMessage(m):
			kind, parent = mentionReview, observation.Event.Data.MessageID
			deadline = observation.Mention.ObservingFrom.Add(mentionObservation)
			if m.SenderID != "" && m.SenderID == observation.Mention.Sender && (m.ResolvedQuote != nil && m.ResolvedQuote.ID == parent || explicitMentionSupplement(m)) {
				kind, deadline = mentionFollowup, arrived.Add(mentionCollection)
			}

		default:
			item.Phase = "skipped"
			return
		}
	}
	if observation != nil {
		if kind != mentionReview {
			observation.Mention.ClosedAt = arrived
		}
		previous := observation.Event.Data.MessageID
		for i := range w.runtime.Inbox {
			prior := &w.runtime.Inbox[i]
			if prior.Mention == nil || prior.Mention.Kind != mentionReview || prior.Mention.Parent != previous || prior.Phase != "received" || prior.Event.Data.ThreadID != m.ThreadID {
				continue
			}
			if kind == mentionReview {
				item.BacklogParent, item.Phase = prior.Event.Data.MessageID, "backlog_grouped"
				return
			}
			// A new explicit interaction takes ownership of the buffered context.
			// The older deadline cannot create a second decision about those rows.
			prior.BacklogParent, prior.Phase = m.ID, "backlog_grouped"
			for j := range w.runtime.Inbox {
				if w.runtime.Inbox[j].BacklogParent == prior.Event.Data.MessageID {
					w.runtime.Inbox[j].BacklogParent = m.ID
				}
			}
			break
		}
	}
	if item.Backlog {
		deadline = now
	}
	item.Mention = &mentionReceipt{Kind: kind, Parent: parent, Sender: m.SenderID, OccurredAt: stamp}
	item.Phase, item.CollectedAt, item.AggregateUntil = "received", arrived, deadline
}

func explicitMentionSupplement(m linuxnative.Message) bool {
	if m.Quote != nil || len(m.Mentions.IDs) > 0 {
		return false
	}
	text := strings.TrimSpace(m.Body)
	for _, p := range []string{"补充一下", "补充：", "补充:", "我刚才说", "我刚刚说", "接着刚才", "刚才那个问题", "我再补充", "更正一下"} {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}
