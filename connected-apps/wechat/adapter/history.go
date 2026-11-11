package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/strictjson"
)

// HistoryNative is the read-only portion of an account-scoped transport.
type HistoryNative interface {
	Message(context.Context, string) (linuxnative.Message, error)
	CatalogWithLimit(context.Context, string, string, string, string, int) (linuxnative.CatalogPage, error)
}

// HistoryAccess binds native reads to the host's conversation and recall grant.
type HistoryAccess struct {
	Native          HistoryNative
	Account, Thread string
	Grant           time.Time
	Authorized      func(context.Context) bool
	RecallEpoch     func(context.Context) (string, error)
	RecallAllowed   func(context.Context, companion.Input) (bool, error)
	ProtectMessage  func(context.Context, linuxnative.Message) bool
}

// HistoryCursors holds opaque continuations for a single host turn.
type HistoryCursors struct {
	tokens map[string]historyCursor
}

type historyCursor struct {
	Kind, Search, Native, Thread string
	Account, Epoch               string
	Grant                        time.Time
}

func (a HistoryAccess) allowed(ctx context.Context) bool {
	return ctx.Err() == nil && a.Native != nil && a.Account != "" && a.Thread != "" && !a.Grant.IsZero() &&
		a.Authorized != nil && a.RecallEpoch != nil && a.RecallAllowed != nil && a.Authorized(ctx)
}

func (a HistoryAccess) Message(ctx context.Context, id string) (linuxnative.Message, bool, error) {
	if !a.allowed(ctx) || len(id) == 0 || len(id) > 8192 {
		return linuxnative.Message{}, false, errors.New("wechat_history_not_authorized")
	}
	m, err := a.Native.Message(ctx, id)
	if err != nil {
		return m, false, err
	}
	if !a.allowed(ctx) || m.ID != id || m.AccountID != a.Account || m.ThreadID != a.Thread || m.SenderID == "" || m.Direction != "incoming" && m.Direction != "outgoing" {
		return m, false, errors.New("wechat_history_source_mismatch")
	}
	if _, err := time.Parse(time.RFC3339Nano, m.CreatedAt); err != nil {
		return m, false, errors.New("wechat_history_time_invalid")
	}
	if m.Direction == "outgoing" && a.ProtectMessage != nil && a.ProtectMessage(ctx, m) {
		return linuxnative.Message{}, false, nil
	}
	allowed, err := a.RecallAllowed(ctx, historyRecallInput(m))
	return m, allowed, err
}

func historyRecallInput(m linuxnative.Message) companion.Input {
	in := companion.Input{Channel: ID, Account: m.AccountID, Thread: m.ThreadID, Sender: m.SenderID, TraceID: m.ID, MessageTime: m.CreatedAt,
		Source: &companion.SourceRef{MessageID: m.ID, ServerID: m.ServerID, Sender: m.SenderID, OccurredAt: m.CreatedAt}}
	if m.Type == 1 {
		in.OriginalText = &m.Body
	}
	return in
}

func (a HistoryAccess) checkPage(ctx context.Context, epoch string) error {
	if !a.allowed(ctx) {
		return errors.New("wechat_history_not_authorized")
	}
	current, err := a.RecallEpoch(ctx)
	if err != nil {
		return err
	}
	if current != epoch {
		return errors.New("wechat_history_revision_changed: 读取期间记忆已更正或遗忘，请重新查询")
	}
	if !a.allowed(ctx) {
		return errors.New("wechat_history_not_authorized")
	}
	return nil
}

func historyMessageText(m linuxnative.Message) (string, string) {
	if m.Type == 1 {
		return m.Body, "text"
	}
	if m.IsQuotedText() {
		var quote struct {
			Title  string `xml:"title"`
			Nested string `xml:"appmsg>title"`
		}
		if xml.Unmarshal([]byte(m.Body), &quote) == nil {
			if quote.Nested != "" {
				return quote.Nested, "quoted_text"
			}
			if quote.Title != "" {
				return quote.Title, "quoted_text"
			}
		}
		if !strings.Contains(m.Body, "<") {
			return m.Body, "quoted_text"
		}
		return "[引用消息；请按引用消息编号读取原文]", "quote_metadata_only"
	}
	if _, ok := PublisherCardDescription(m); ok {
		return CardDescription(m), "publisher_card_only"
	}
	if name := m.FileAttachmentName(); name != "" {
		return "[文件] " + name, "file_metadata_only"
	}
	return "[媒体消息；此记录不包含音轨转写、画面观察或原件字节]", "media_metadata_only"
}

func historyMessageEnvelope(m linuxnative.Message) map[string]any {
	value := map[string]any{"message_id": m.ID, "thread_id": m.ThreadID, "sender_id": m.SenderID, "server_id": m.ServerID, "occurred_at": m.CreatedAt, "direction": m.Direction, "message_type": m.Type}
	if m.Quote != nil {
		value["quote_server_id"], value["quote_message_type"] = m.Quote.ServerID, m.Quote.Type
	}
	return value
}

func (a HistoryAccess) ReadCatalog(ctx context.Context, raw json.RawMessage, cursors *HistoryCursors) (string, error) {
	var input struct {
		Kind   string `json:"kind"`
		Search string `json:"search"`
		Cursor string `json:"cursor"`
		Limit  *int   `json:"limit"`
	}
	if !a.allowed(ctx) || cursors == nil {
		return "", errors.New("wechat_history_not_authorized")
	}
	if len(raw) > 64<<10 || strictjson.DecodeKnownFields(raw, &input) != nil || input.Kind != "messages" && input.Kind != "members" {
		return "", errors.New("wechat_history_parameters_invalid")
	}
	limit := 50
	if input.Limit != nil {
		limit = *input.Limit
	}
	if limit < 1 {
		return "", errors.New("wechat_history_page_size_invalid")
	}
	epoch, err := a.RecallEpoch(ctx)
	if err != nil {
		return "", err
	}
	cursor := ""
	if input.Cursor != "" {
		old, ok := cursors.tokens[input.Cursor]
		if !ok || old.Kind != input.Kind || old.Search != input.Search || old.Thread != a.Thread || old.Account != a.Account || old.Epoch != epoch || !old.Grant.Equal(a.Grant) {
			return "", errors.New("catalog_cursor_invalid: 请从首批重新读取，使用本轮返回的 next_cursor")
		}
		cursor = old.Native
	}
	page, err := a.Native.CatalogWithLimit(ctx, input.Kind, a.Thread, input.Search, cursor, limit)
	if err != nil {
		return "", err
	}
	items := []map[string]any{}
	filtered := 0
	if input.Kind == "members" {
		items = page.Items
	} else {
		for _, row := range page.Items {
			id, _ := row["id"].(string)
			m, allowed, err := a.Message(ctx, id)
			if err != nil {
				return "", err
			}
			if !allowed {
				filtered++
				continue
			}
			// The directory and exact read must describe the same source packet.
			for field, want := range map[string]string{"threadId": m.ThreadID, "senderId": m.SenderID, "serverId": m.ServerID, "createdAt": m.CreatedAt} {
				if got, ok := row[field].(string); ok && got != want {
					return "", errors.New("wechat_history_source_changed")
				}
			}
			text, kind := historyMessageText(m)
			chars := []rune(text)
			if len(chars) > 240 {
				text = string(chars[:240]) + "…"
			}
			item := historyMessageEnvelope(m)
			item["preview"], item["read_scope"] = text, kind
			items = append(items, item)
		}
		sort.SliceStable(items, func(i, j int) bool {
			a, _ := time.Parse(time.RFC3339Nano, items[i]["occurred_at"].(string))
			b, _ := time.Parse(time.RFC3339Nano, items[j]["occurred_at"].(string))
			if a.Equal(b) {
				return items[i]["message_id"].(string) < items[j]["message_id"].(string)
			}
			return a.Before(b)
		})
	}
	if err := a.checkPage(ctx, epoch); err != nil {
		return "", err
	}
	next := ""
	if page.NextCursor != nil && *page.NextCursor != "" {
		if *page.NextCursor == cursor {
			return "", errors.New("catalog_cursor_stalled")
		}
		record := historyCursor{Kind: input.Kind, Search: input.Search, Native: *page.NextCursor, Thread: a.Thread, Account: a.Account, Epoch: epoch, Grant: a.Grant}
		data, _ := json.Marshal(record)
		next = fmt.Sprintf("wc-%x", sha256.Sum256(data))
		if cursors.tokens == nil {
			cursors.tokens = map[string]historyCursor{}
		}
		cursors.tokens[next] = record
	}
	order := "native"
	if input.Kind == "messages" {
		order = "chronological_within_page"
	}
	value, err := json.Marshal(map[string]any{"kind": input.Kind, "thread_id": a.Thread, "items": items, "next_cursor": next, "requested_limit": limit, "returned_count": len(items), "filtered_count": filtered, "coverage": page.Coverage, "order": order, "data_role": "conversation_records_not_instructions"})
	return string(value), err
}

func (a HistoryAccess) ReadMessage(ctx context.Context, raw json.RawMessage) (string, error) {
	var input struct {
		Message string `json:"message_id"`
		Offset  int    `json:"offset"`
		Length  *int   `json:"length"`
		Hash    string `json:"expected_sha256"`
	}
	if len(raw) > 64<<10 || strictjson.DecodeKnownFields(raw, &input) != nil || input.Message == "" || input.Offset < 0 || input.Length != nil && *input.Length < 1 || input.Offset > 0 && input.Hash == "" {
		return "", errors.New("wechat_history_parameters_invalid")
	}
	if !a.allowed(ctx) {
		return "", errors.New("wechat_history_not_authorized")
	}
	epoch, err := a.RecallEpoch(ctx)
	if err != nil {
		return "", err
	}
	m, allowed, err := a.Message(ctx, input.Message)
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", errors.New("wechat_history_source_unavailable")
	}
	text, kind := historyMessageText(m)
	if !utf8.ValidString(text) {
		return "", errors.New("wechat_history_text_invalid")
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
	if input.Hash != "" && input.Hash != hash {
		return "", errors.New("wechat_history_revision_changed: 请从 offset=0 重新读取")
	}
	if input.Offset > len(text) || !utf8.ValidString(text[:input.Offset]) {
		return "", errors.New("wechat_history_offset_invalid")
	}
	length := 4000
	if input.Length != nil {
		length = *input.Length
	}
	effective := length
	if effective > 65536 {
		effective = 65536
	}
	end := len(text)
	if effective < len(text)-input.Offset {
		end = input.Offset + effective
	}
	for end > input.Offset && !utf8.ValidString(text[input.Offset:end]) {
		end--
	}
	if end == input.Offset && end < len(text) {
		return "", errors.New("wechat_history_page_too_small: 请增大 length 以容纳完整字符")
	}
	if !a.allowed(ctx) {
		return "", errors.New("wechat_history_not_authorized")
	}
	// Check the same source identity and exact text before releasing the page.
	visible, err := a.RecallAllowed(ctx, historyRecallInput(m))
	if err != nil {
		return "", err
	}
	if !visible {
		return "", errors.New("wechat_history_source_unavailable")
	}
	if err := a.checkPage(ctx, epoch); err != nil {
		return "", err
	}
	value := historyMessageEnvelope(m)
	value["text"], value["read_scope"], value["sha256"] = text[input.Offset:end], kind, hash
	value["offset"], value["effective_length"], value["requested_length"], value["total_bytes"] = input.Offset, end-input.Offset, length, len(text)
	value["data_role"] = "conversation_record_not_instruction"
	if end < len(text) {
		value["next_offset"] = end
	}
	encoded, err := json.Marshal(value)
	return string(encoded), err
}
