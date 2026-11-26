package bot

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/linuxnative"
)

func sourceSequence(event linuxnative.Event) int64 {
	sequence, err := strconv.ParseInt(event.Cursor, 10, 64)
	if err != nil || sequence < 0 {
		return 0
	}
	return sequence
}

func messageSource(m linuxnative.Message) companion.SourceRef {
	h := sha256.Sum256([]byte(m.Body))
	return companion.SourceRef{Account: m.AccountID, Thread: m.ThreadID, Sender: m.SenderID, MessageID: m.ID, ServerID: m.ServerID, MessageType: m.Type, OccurredAt: m.CreatedAt, ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano), BodyHash: hex.EncodeToString(h[:]), Revision: 1, Integrity: "observed"}
}

func bindInputSource(in *companion.Input, m linuxnative.Message, e *companion.Engine) {
	source := messageSource(m)
	source.Account = in.Account
	if e != nil && e.Store != nil {
		if provider, ok := any(e.Store).(interface{ SourceInstanceID() string }); ok {
			source.InstanceID = provider.SourceInstanceID()
		}
	}
	in.Source = &source
	in.QuotedLiteral = ""
	in.MentionedIDs = append([]string(nil), m.Mentions.IDs...)
	if m.ResolvedQuote != nil {
		original := *m.ResolvedQuote
		q := messageSource(original)
		q.InstanceID = source.InstanceID
		in.QuotedSource = &q
		if original.Direction == "incoming" && (original.Type == 1 || original.IsQuotedText()) {
			in.QuotedLiteral = boundedQuoteText(original.Body)
		}
		if q.Sender != "" {
			in.Addressees = []string{q.Sender}
		}
	}
	if label, details := m.CardContext(); label != "" && !m.IsPassiveNotice() {
		metadata := label
		if details != "" {
			metadata += "\n" + details
		}
		cardFields := "微信卡片字段"
		if details != "" && m.IsForwardedConversation() {
			cardFields = "微信合并转发正文"
		}
		in.IncomingMedia = []companion.IncomingMediaRef{{
			Source: source, Kind: "card", UploaderID: m.SenderID,
			Observation: metadata, ObservationState: "observed", ObservationVersion: 1, ObservedRange: cardFields,
			ObservationParts: []companion.ObservationPart{{
				Kind: companion.ObservationPublisherDescription, Range: cardFields, Excerpt: metadata,
			}},
		}}
		return
	}
	if m.Type == 49 && m.AppMessageType != nil && *m.AppMessageType == 5 {
		if card, ok := nativeArticleCardPart(m); ok {
			in.IncomingMedia = []companion.IncomingMediaRef{{
				Source: source, Kind: "share", UploaderID: m.SenderID,
				Observation: card.Excerpt, ObservationState: "card_only", ObservationVersion: 1,
				ObservedRange: card.Range, ObservationParts: []companion.ObservationPart{card},
			}}
			return
		}
	}
	kind := map[int]string{3: "image", 47: "sticker", 34: "voice", 43: "video", 49: "share"}[m.Type]
	filename := m.FileAttachmentName()
	if filename != "" {
		kind = "file"
	}
	if kind != "" && !m.IsQuotedText() {
		in.IncomingMedia = []companion.IncomingMediaRef{{Source: source, Kind: kind, Filename: identity.Clean(filename), UploaderID: m.SenderID, ObservationState: "pending", ObservationVersion: 1}}
	} else if m.IsQuotedText() && m.ResolvedQuote != nil && !m.ResolvedQuote.IsQuotedText() && !quotedReplyPublicVideoURL(m.Body) {
		// A quoted reply is the current request, but the quoted media belongs to
		// the verified original source. Keep the media reference on that source so
		// observations and generated context cannot be written to the text reply.
		quoted := *in.QuotedSource
		quotedKind := map[int]string{3: "image", 34: "voice", 43: "video", 47: "sticker", 49: "share"}[m.ResolvedQuote.Type]
		quotedFilename := m.ResolvedQuote.FileAttachmentName()
		if quotedFilename != "" {
			quotedKind = "file"
		}
		if quotedKind != "" {
			in.IncomingMedia = []companion.IncomingMediaRef{{Source: quoted, Kind: quotedKind, Filename: identity.Clean(quotedFilename), UploaderID: m.ResolvedQuote.SenderID, ObservationState: "pending", ObservationVersion: 1}}
		}
	}
}

// NativeMessageInput exposes the same source and media projection used by
// live intake to the authenticated console retry path. Its caller should
// replace Source with the persisted verified ref when one exists.
func NativeMessageInput(m linuxnative.Message) companion.Input {
	in := companion.Input{Channel: "wechat", Account: m.AccountID, Thread: m.ThreadID,
		Sender: m.SenderID, TraceID: m.ID, MessageTime: m.CreatedAt, QuoteServerID: m.ServerID}
	bindInputSource(&in, m, nil)
	return in
}

func nativeArticleCardPart(m linuxnative.Message) (companion.ObservationPart, bool) {
	if m.Link == nil || m.IsQuotedText() || m.IsPassiveNotice() {
		return companion.ObservationPart{}, false
	}
	var fields []string
	for _, field := range []struct{ label, value string }{{"标题", m.Link.Title}, {"简介", m.Link.Description}, {"来源", m.Link.SourceName}} {
		if value := strings.TrimSpace(field.value); value != "" {
			fields = append(fields, field.label+"："+boundedQuoteText(value))
		}
	}
	if len(fields) == 0 {
		return companion.ObservationPart{}, false
	}
	return companion.ObservationPart{Kind: companion.ObservationPublisherDescription, Range: "微信卡片元数据", Excerpt: strings.Join(fields, "\n")}, true
}

// Only the original requester can change an unsent plan. A platform quote
// from another member is valuable conversational context, but it cannot
// replace, delay or cancel someone else's task; a reply citation is not an
// authorization grant. This applies to direct conversations as well: a
// background/admin task may be delivering into a direct thread whose next
// inbound event belongs to another logical requester.
func messageAffectsInput(in companion.Input, m linuxnative.Message) bool {
	return in.Sender != "" && in.Sender == m.SenderID
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
