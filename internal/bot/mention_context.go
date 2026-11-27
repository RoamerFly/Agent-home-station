package bot

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type mentionWindowSource interface {
	MessageWindow(context.Context, string, string, int, int) (linuxnative.MessageWindow, error)
}

// An unseen native message may be used as context only while all current
// host grants and memory barriers still admit that exact source.
func (w *Worker) mentionHistoryVisible(ctx context.Context, m linuxnative.Message, thread string) bool {
	if m.ID == "" || m.ThreadID != thread || m.AccountID != w.source.Account() || m.SenderID == "" || m.Direction != "incoming" && m.Direction != "outgoing" || m.IsPassiveNotice() || !w.allowed[thread] {
		return false
	}
	if w.config.MentionHistoryAllowed != nil && !w.config.MentionHistoryAllowed(ctx, m) {
		return false
	}
	if m.Direction == "outgoing" && w.config.ProtectQuote != nil && w.config.ProtectQuote(ctx, m) {
		return false
	}
	c, ok := w.model.(*companion.Engine)
	if !ok || c.Store == nil {
		return false
	}
	in := NativeMessageInput(m)
	if m.Type == 1 {
		in.OriginalText = &m.Body
	}
	allowed, err := c.Store.NativeRecallAllowed(ctx, in)
	if err != nil || !allowed {
		return false
	}
	if !c.Store.LegacyHistoryAllowed(m.AccountID, thread) {
		if item := w.mentionItem(thread, m.ID); item != nil && item.RecallGeneration == c.Store.NativeRecallGeneration(m.AccountID, thread) {
			return true
		}
		_, present := c.Store.SourceForInput(in)
		return present
	}
	return true
}

func (w *Worker) mentionWindow(ctx context.Context, center linuxnative.Message, n int) linuxnative.MessageWindow {
	if native, ok := w.source.(mentionWindowSource); ok {
		window, _ := native.MessageWindow(ctx, center.ThreadID, center.ID, n, n)
		if window.Anchor.ID == center.ID && window.Anchor.AccountID == center.AccountID && window.Anchor.ThreadID == center.ThreadID {
			return window
		}
	}
	win := linuxnative.MessageWindow{Anchor: center, Coverage: "partial_durable_inbox"}
	if w.runtime == nil || n <= 0 {
		return win
	}
	at, err := time.Parse(time.RFC3339Nano, center.CreatedAt)
	if err != nil {
		return win
	}
	anchorIndex := -1
	var items []linuxnative.Message
	for i := range w.runtime.Inbox {
		item := &w.runtime.Inbox[i]
		if item.Event.Data.ThreadID != center.ThreadID {
			continue
		}
		id := item.Event.Data.MessageID
		if id == center.ID {
			anchorIndex = len(items)
			items = append(items, center)
			continue
		}
		if ctx.Err() != nil {
			break
		}
		m, err := w.source.Message(ctx, id)
		if err == nil && m.ID == id && m.ThreadID == center.ThreadID && m.AccountID == center.AccountID {
			items = append(items, m)
		}
	}
	for i, m := range items {
		if m.ID == center.ID {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, m.CreatedAt)
		if err != nil {
			continue
		}
		switch {
		case t.Before(at) || t.Equal(at) && anchorIndex >= 0 && i < anchorIndex:
			win.Before = append(win.Before, m)
		case t.After(at) || t.Equal(at) && anchorIndex >= 0 && i > anchorIndex:
			win.After = append(win.After, m)
		}
	}
	for _, rows := range [][]linuxnative.Message{win.Before, win.After} {
		sort.SliceStable(rows, func(i, j int) bool {
			a, _ := time.Parse(time.RFC3339Nano, rows[i].CreatedAt)
			b, _ := time.Parse(time.RFC3339Nano, rows[j].CreatedAt)
			return a.Before(b)
		})
	}
	if len(win.Before) > n {
		win.Before = win.Before[len(win.Before)-n:]
	}
	if len(win.After) > n {
		win.After = win.After[:n]
	}
	return win
}

func (w *Worker) addMentionContext(ctx context.Context, c *companion.Engine, m linuxnative.Message, receipt *mentionReceipt, in *companion.Input) {
	in.MentionContext = "当前群聊采用提及模式，原生上下文未能读取。"
	if receipt != nil && receipt.Parent != "" {
		in.Directed = false
		in.MentionContext += " 仅评估观察窗内与自己的互动；无关则 silent。"
	}
	read, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	epoch, err := c.Store.RecallEpoch(read)
	if err != nil {
		return
	}
	n := 5
	if setting := w.config.GroupRules[m.ThreadID].MentionContextEachSide; setting != nil {
		n = *setting
	}
	var rows []companion.ConversationEvent
	seen := map[string]bool{m.ID: true}
	add := func(row linuxnative.Message, relation string) {
		if seen[row.ID] || !w.mentionHistoryVisible(read, row, m.ThreadID) {
			return
		}
		seen[row.ID] = true
		event := conversationEvent(row, false)
		event.Relation = relation + "_" + row.Direction
		event.NativeWindow, event.Direction = true, row.Direction
		event.NativeRecallGeneration = c.Store.NativeRecallGeneration(in.Account, in.Thread)
		if item := w.mentionItem(in.Thread, row.ID); item != nil {
			event.Source.Sequence = sourceSequence(item.Event)
		}
		rows = append(rows, event)
	}
	var coverage []string
	around := func(center linuxnative.Message, relation string) {
		if n <= 0 {
			if center.ID != m.ID {
				add(center, relation+"_anchor")
			}
			return
		}
		win := w.mentionWindow(read, center, n)
		coverage = append(coverage, relation+"="+win.Coverage)
		for _, row := range win.Before {
			add(row, relation+"_before")
		}
		if center.ID != m.ID {
			add(center, relation+"_anchor")
		}
		// A review sees the conversation leading up to the first new
		// speaker, including the preceding reply. Its later rows come only
		// from actual receipts admitted to this observation window.
		if receipt == nil || receipt.Kind != mentionReview {
			for _, row := range win.After {
				add(row, relation+"_after")
			}
		}
	}
	// A verified quotation predates its referencing request, including when both
	// share the same platform timestamp. Merge the older window first.
	if m.ResolvedQuote != nil && w.mentionHistoryVisible(read, *m.ResolvedQuote, m.ThreadID) {
		around(*m.ResolvedQuote, "quoted_message")
	}
	around(m, "request")
	if receipt != nil && w.runtime != nil {
		for i := range w.runtime.Inbox {
			item := &w.runtime.Inbox[i]
			id := item.Event.Data.MessageID
			if item.Event.Data.ThreadID != m.ThreadID || item.BacklogParent != m.ID && id != receipt.Parent || seen[id] {
				continue
			}
			if read.Err() != nil {
				break
			}
			peer, err := w.source.Message(read, id)
			if err == nil && peer.ID == id {
				add(peer, "observation")
			}
		}
	}
	after, err := c.Store.RecallEpoch(read)
	if err != nil || after != epoch {
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339Nano, rows[i].At)
		b, _ := time.Parse(time.RFC3339Nano, rows[j].At)
		if a.Equal(b) && rows[i].Source.Sequence > 0 && rows[j].Source.Sequence > 0 {
			return rows[i].Source.Sequence < rows[j].Source.Sequence
		}
		return a.Before(b)
	})
	in.UnreadMessages = rows
	in.MentionContext = fmt.Sprintf("群聊上下文每侧请求 %d 条。按实际消息时间、发送者和引用理解；前后消息是背景，各人只代表自己的发言。", n)
	if len(coverage) > 0 {
		in.MentionContext += " 覆盖：" + strings.Join(coverage, ", ")
	}
	if receipt != nil && receipt.Parent != "" {
		in.Directed = false
		in.MentionContext += "\n这是刚才群聊互动的观察窗口。结合之前的请求和本批消息判断是否有人在接着与你说话。若只是群友之间的话题，选择 silent。"
	}
}
