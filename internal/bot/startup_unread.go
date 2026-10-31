package bot

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

// Read to the event tail before evaluating offline messages. A full page is not
// a conversation boundary. Cursor advances only after its message is persisted.
func (w *Worker) captureStartupUnread(ctx context.Context, path string, state *checkpoint) error {
	for !w.draining() && ctx.Err() == nil {
		page, err := w.source.Events(ctx, state.Cursor)
		if err != nil {
			return err
		}
		for _, event := range page.Items {
			duplicate := state.contains(event.Data.MessageID)
			for _, old := range w.runtime.Inbox {
				if old.Event.Data.MessageID == event.Data.MessageID {
					duplicate = true
					break
				}
			}
			if event.Kind == "message.committed" && event.Data.MessageID != "" && !duplicate && event.Data.Origin != "local_send" {
				if len(w.runtime.Inbox) >= 4000 {
					w.pruneRuntime()
					if len(w.runtime.Inbox) >= 5000 {
						return errors.New("startup inbox full; source cursor retained for continuation")
					}
				}
				w.runtime.Inbox = append(w.runtime.Inbox, inboxItem{Event: event, Phase: "received", Backlog: true, Updated: time.Now().UTC()})
				if err = w.saveRuntime(); err != nil {
					return err
				}
			}
			state.Cursor = event.Cursor
			if event.Data.MessageID != "" {
				state.remember(event.Data.MessageID)
			}
			if err = saveCheckpoint(path, *state); err != nil {
				return err
			}
		}
		if len(page.Items) < 100 {
			break
		}
	}
	for i := range w.runtime.Inbox {
		item := &w.runtime.Inbox[i]
		if !item.ToolStarted && (item.Phase == "received" || item.Phase == "read_pending" || item.Phase == "processing" || item.Phase == "backlog_grouped") {
			item.Backlog = true
		}
	}
	return w.saveRuntime()
}

func (w *Worker) processUnreadConversation(ctx context.Context, thread string) error {
	c, ok := w.model.(*companion.Engine)
	if !ok {
		return nil
	}
	if w.generation != nil && (w.generation.jobs[thread] != nil || !w.generation.canGenerate()) {
		return nil
	}
	if w.config.AuthorizeThread != nil {
		check, cancel := context.WithTimeout(ctx, 10*time.Second)
		allowed, err := w.config.AuthorizeThread(check, thread)
		cancel()
		if err != nil {
			return nil
		}
		if allowed {
			w.allowed[thread] = true
		} else {
			delete(w.allowed, thread)
		}
	}
	var messages []linuxnative.Message
	for i := range w.runtime.Inbox {
		item := &w.runtime.Inbox[i]
		if !item.Backlog || item.Event.Data.ThreadID != thread || item.ToolStarted || time.Now().Before(item.RetryAfter) {
			continue
		}
		if item.Phase != "received" && item.Phase != "read_pending" && item.Phase != "processing" && item.Phase != "backlog_grouped" {
			continue
		}
		read, cancel := context.WithTimeout(ctx, 10*time.Second)
		m, err := w.source.Message(read, item.Event.Data.MessageID)
		cancel()
		if err != nil {
			w.queuePhase(item.Event.Data.MessageID, "read_pending")
			return w.persistErr
		}
		if m.ThreadID != thread || m.ID != item.Event.Data.MessageID || m.Direction != item.Event.Data.Direction {
			w.queuePhase(item.Event.Data.MessageID, "skipped")
			continue
		}
		w.recordIncoming(m)
		if !w.allowed[thread] {
			w.queuePhase(m.ID, "skipped")
			continue
		}
		messages = append(messages, m)
	}
	if len(messages) == 0 {
		return w.persistErr
	}
	sort.SliceStable(messages, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339, messages[i].CreatedAt)
		b, _ := time.Parse(time.RFC3339, messages[j].CreatedAt)
		return a.Before(b)
	})
	now := time.Now()
	unread := []companion.ConversationEvent{}
	mediaIDs := []string{}
	var anchor *linuxnative.Message
	var allIDs []string
	w.recovering = true
	defer func() { w.recovering = false }()
	for _, m := range messages {
		created, err := time.Parse(time.RFC3339, m.CreatedAt)
		if err != nil || created.After(now.Add(30*time.Second)) || now.Sub(created) > w.maxMessageAge() {
			w.queuePhase(m.ID, "skipped")
			continue
		}
		if m.Direction != "incoming" && !(thread == "filehelper" && m.Direction == "outgoing") {
			w.queuePhase(m.ID, "completed")
			continue
		}
		working := m
		isMedia := m.Type == 3 || m.Type == 34 || m.Type == 43 || m.Type == 47 || m.Type == 49 && !m.IsQuotedText()
		body := messageSummary(m)
		original := ""
		if m.Type == 1 {
			original = m.Body
		}
		if m.IsQuotedText() {
			original = quotedPlainBody(m).Body
			body = original + w.readQuoteContext(ctx, &m)
			working.Type = 1
			working.Body = body
		}
		if isMedia {
			working.Type = 1
			working.Body = "收到媒体"
			if !strings.HasSuffix(thread, "@chatroom") {
				working.Body = w.config.Prefix + working.Body
			}
		}
		_, eligible := w.prompt(working, now)
		w.noticeConversationMessage(m, eligible)
		w.stopRequestedVoice(ctx, working)
		if eligible {
			copy := m
			anchor = &copy
		}
		allIDs = append(allIDs, m.ID)
		if isMedia && eligible && w.observationAllowed(m) && w.config.Analyzer != nil {
			mediaIDs = append(mediaIDs, m.ID)
		}
		observed := companion.Input{Channel: w.config.Channel, Account: w.source.Account(), Thread: thread, Sender: m.SenderID, TraceID: m.ID, Text: body, MessageTime: m.CreatedAt, OriginalText: &original}
		bindInputSource(&observed, m, c)
		if err = c.Store.Observe(observed, c.Config, now); err != nil {
			return err
		}
		unread = append(unread, companion.ConversationEvent{MessageID: m.ID, Sender: m.SenderID, Text: body, At: m.CreatedAt, NewRequest: eligible, Source: observed.Source, ReplyTo: observed.QuotedSource})
	}
	if anchor == nil {
		for _, id := range allIDs {
			w.queuePhase(id, "skipped")
		}
		return w.persistErr
	}
	// Use a real source message for routing/authorization. Other speakers remain
	// contextual data, never aliases for the anchor's authenticated sender.
	in := companion.Input{Channel: w.config.Channel, Account: w.source.Account(), Thread: thread, Sender: anchor.SenderID, TraceID: anchor.ID, MessageTime: anchor.CreatedAt, QuoteServerID: anchor.ServerID, Mode: "social", IsGroup: strings.HasSuffix(thread, "@chatroom"), Directed: w.addressed(*anchor), PersonaOverride: w.config.GroupRules[thread].Persona, UnreadMessages: unread, UnreadMediaIDs: mediaIDs, HistoryObserved: true,
		Text: "这是重新上线后本会话尚未查看的一段未读消息。按unread_messages里的真实时间和发言者整体理解，结合后续消息判断哪些事项仍需要回复或行动；不逐条追着旧话补答。自然回应，也可以安静。"}
	if !in.IsGroup || in.Directed {
		in.Mode = "direct"
	}
	if in.Directed && anchor.Mentions.Self != nil && *anchor.Mentions.Self {
		in.MustReply = w.config.GroupRules[thread].ForceReplyWhenMentioned
	}
	for i := range w.runtime.Inbox {
		item := &w.runtime.Inbox[i]
		for _, id := range allIDs {
			if item.Event.Data.MessageID == id {
				item.BacklogParent = anchor.ID
				item.Phase = "backlog_grouped"
				if id == anchor.ID {
					item.Phase = "received"
				}
				break
			}
		}
	}
	if err := w.saveRuntime(); err != nil {
		return err
	}
	for _, id := range allIDs {
		if id != anchor.ID {
			w.activity(Record{Event: "backlog_message_grouped", Category: "decision", ThreadID: thread, MessageID: id, RelatedMessageID: anchor.ID, Reason: "offline_conversation_batch", Status: "aggregated"})
		}
	}
	w.activity(Record{Event: "backlog_conversation_started", Category: "decision", ThreadID: thread, MessageID: anchor.ID, Content: "按会话综合处理未读消息", Reason: "offline_conversation_batch", Status: "processing"})
	if w.generation != nil {
		w.startGeneration(ctx, c, in, nil)
	} else {
		w.respondCharacter(ctx, c, in)
	}
	return w.persistErr
}
