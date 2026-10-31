package bot

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

func messageSource(m linuxnative.Message) companion.SourceRef {
	h := sha256.Sum256([]byte(m.Body))
	return companion.SourceRef{Account: m.AccountID, Thread: m.ThreadID, Sender: m.SenderID, MessageID: m.ID, ServerID: m.ServerID, MessageType: m.Type, OccurredAt: m.CreatedAt, ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano), BodyHash: hex.EncodeToString(h[:]), Revision: 1, Integrity: "observed"}
}

func bindInputSource(in *companion.Input, m linuxnative.Message, e *companion.Engine) {
	source := messageSource(m)
	source.Account = in.Account
	if provider, ok := any(e.Store).(interface{ SourceInstanceID() string }); ok {
		source.InstanceID = provider.SourceInstanceID()
	}
	in.Source = &source
	in.MentionedIDs = append([]string(nil), m.Mentions.IDs...)
	if m.ResolvedQuote != nil {
		q := messageSource(*m.ResolvedQuote)
		q.InstanceID = source.InstanceID
		in.QuotedSource = &q
		if q.Sender != "" {
			in.Addressees = []string{q.Sender}
		}
	}
	kind := map[int]string{3: "image", 47: "sticker", 34: "voice", 43: "video", 49: "share"}[m.Type]
	if kind != "" && !m.IsQuotedText() {
		in.IncomingMedia = []companion.IncomingMediaRef{{Source: source, Kind: kind, UploaderID: m.SenderID, ObservationState: "pending", ObservationVersion: 1}}
	}
}

// Only same-author followups or a verified platform quote affect a pending
// plan. Ambient group chat can require a visible quote but cannot rewrite it.
func messageAffectsInput(in companion.Input, m linuxnative.Message) bool {
	if !strings.HasSuffix(m.ThreadID, "@chatroom") {
		return true
	}
	if in.Sender != "" && in.Sender == m.SenderID {
		return true
	}
	return m.ResolvedQuote != nil && m.ResolvedQuote.ID == in.TraceID
}

func conversationEvent(m linuxnative.Message, eligible bool) companion.ConversationEvent {
	s := messageSource(m)
	e := companion.ConversationEvent{MessageID: m.ID, Sender: m.SenderID, Text: messageSummary(m), At: m.CreatedAt, NewRequest: eligible, Source: &s, Relation: "same_sender_followup"}
	if m.ResolvedQuote != nil {
		q := messageSource(*m.ResolvedQuote)
		e.ReplyTo = &q
		e.Relation = "verified_quote"
	}
	return e
}
