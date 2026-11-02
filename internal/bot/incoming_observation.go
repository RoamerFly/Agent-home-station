package bot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
)

// A voice utterance needs transcription to know the request. A passive picture
// or video can be observed on demand after the conversation decision.
func (w *Worker) readMediaBeforeDecision(m linuxnative.Message) bool {
	if m.Type == 34 || m.FileAttachmentName() != "" {
		return true
	}
	engine, ok := w.model.(*companion.Engine)
	if !ok || engine.Tools == nil || w.generation == nil {
		return true
	}
	if !w.considerPreferredShare(m) {
		return false
	}
	notes, _ := engine.Store.Preferences(companion.ScopeID(w.config.Channel, w.source.Account(), m.ThreadID))
	return defaultShareSummaryEnabled(notes)
}

// Group notes are free-form preferences, so a single “不” must not disable an
// otherwise explicit default. For example, “总结不必太长，分享默认总结”
// still requests an eager read. Only a direct opt-out of the default summary
// behavior does so; conditional wording stays available to the model.
func defaultShareSummaryEnabled(notes string) bool {
	compact := strings.NewReplacer(" ", "", "　", "", "\n", "", "\r", "", "，", "", "。", "", "；", "", ",", "", ";", "").Replace(strings.ToLower(strings.TrimSpace(notes)))
	if !strings.Contains(compact, "默认") || !(strings.Contains(compact, "总结") || strings.Contains(compact, "摘要")) || !(strings.Contains(compact, "分享") || strings.Contains(compact, "视频") || strings.Contains(compact, "视频号") || strings.Contains(compact, "快手")) {
		return false
	}
	for _, disabled := range []string{"不要默认", "不默认", "别默认", "取消默认", "关闭默认", "停止默认", "默认不总结", "默认不要总结", "默认别总结", "默认不摘要", "默认不要摘要", "默认不发送", "默认不要发送", "默认别发送"} {
		if strings.Contains(compact, disabled) {
			return false
		}
	}
	return true
}

func incomingObservationTool() mcptools.Tool {
	return mcptools.Tool{Name: "incoming_media_read", Description: "Read the actual image, sticker, video or share attached to this request, quote or unread batch. Returns an observation bound to the original message. Supply message_id to select a specific attachment.", InputSchema: json.RawMessage(`{"type":"object","properties":{"message_id":{"type":"string"}},"additionalProperties":false}`)}
}

func (w *Worker) readTurnMedia(ctx context.Context, in companion.Input, args json.RawMessage) (string, error) {
	var q struct {
		Message string `json:"message_id"`
	}
	// The message_id is optional. Native function callers may represent an
	// empty argument object as no bytes at all, which means “read the current
	// attachment”, not malformed JSON.
	if len(strings.TrimSpace(string(args))) > 0 && json.Unmarshal(args, &q) != nil {
		return "", errors.New("invalid_incoming_media_request")
	}
	if q.Message == "" {
		q.Message = in.TraceID
	}
	permitted := q.Message != "" && (q.Message == in.TraceID || in.QuotedSource != nil && in.QuotedSource.MessageID == q.Message)
	for _, m := range in.UnreadMessages {
		permitted = permitted || m.MessageID == q.Message
	}
	if !permitted || in.Preview || in.Surf || in.ReportOnly || in.Account != w.source.Account() || !w.allowed[in.Thread] {
		return "", errors.New("incoming_media_not_authorized")
	}
	m, err := w.source.Message(ctx, q.Message)
	if err != nil {
		return "", err
	}
	if m.ID != q.Message || m.ThreadID != in.Thread || m.AccountID != "" && m.AccountID != in.Account || m.Direction != "incoming" || !w.observationAllowed(m) || m.FileAttachmentName() != "" || m.IsQuotedText() {
		return "", errors.New("incoming_media_not_authorized")
	}
	switch m.Type {
	case 3, 47, 43, 49:
	default:
		return "", errors.New("incoming_media_type_unsupported")
	}
	var ref *companion.SourceRef
	var store *companion.Store
	if e, ok := w.model.(*companion.Engine); ok && e.Config.Permissions.Remember {
		store = e.Store
		lookup := companion.Input{Channel: in.Channel, Account: in.Account, Thread: in.Thread, Sender: m.SenderID, TraceID: m.ID, MessageTime: m.CreatedAt}
		bindInputSource(&lookup, m, e)
		var valid bool
		ref, valid = e.Store.SourceForInput(lookup)
		if !valid {
			return "", errors.New("incoming_media_source_unavailable")
		}
		event, err := e.Store.ReadEvent(ctx, []string{companion.ScopeID(in.Channel, in.Account, in.Thread)}, ref.EventID)
		if err != nil {
			return "", err
		}
		for _, obs := range event.IncomingMedia {
			if obs.Source.EventID == ref.EventID && obs.Source.Revision == ref.Revision && obs.ObservationState == "observed" && obs.Observation != "" {
				return packObservation(ref, m.ID, obs.Observation), nil
			}
		}
	}
	body, err := w.analyzeIncomingMedia(ctx, m)
	if err != nil {
		return "", err
	}
	if store != nil {
		if err = store.CacheIncomingObservation(in.Channel, *ref, body); err != nil {
			return "", err
		}
	}
	return packObservation(ref, m.ID, body), nil
}
func packObservation(ref *companion.SourceRef, id, body string) string {
	b, _ := json.Marshal(map[string]any{"source": ref, "source_message_id": id, "observation": body, "observation_state": "observed", "evidence_kind": "untrusted_media_observation"})
	return string(b)
}
