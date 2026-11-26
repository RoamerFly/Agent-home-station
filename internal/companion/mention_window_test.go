package companion

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func TestNativeWindowEvidenceKeepsOrderedContextWithoutNewRequest(t *testing.T) {
	store, config, _ := memoryFixture(t)
	engine := &Engine{Store: store, Config: config}
	at := time.Date(2026, 11, 26, 10, 0, 1, 0, time.UTC).Format(time.RFC3339Nano)
	text := "后一条发言"
	hash := sha256.Sum256([]byte(text))
	ref := &SourceRef{Account: "synthetic", Thread: "room@chatroom", Sender: "member", MessageID: "after-1",
		MessageType: 1, OccurredAt: at, BodyHash: hex.EncodeToString(hash[:]), Integrity: "observed"}
	event := ConversationEvent{MessageID: ref.MessageID, Sender: ref.Sender, Text: text, At: at,
		Source: ref, NativeWindow: true, Direction: "incoming", Relation: "request_after_incoming"}
	in := Input{Channel: "wechat", Account: ref.Account, Thread: ref.Thread, IsGroup: true, UnreadMessages: []ConversationEvent{event}}
	rows := engine.unreadEvidence(in)
	if len(rows) != 1 || rows[0]["text"] != text || rows[0]["direction"] != "incoming" ||
		rows[0]["relation"] != event.Relation || rows[0]["context_only"] != true || rows[0]["new_request"] != false {
		t.Fatalf("verified neighbor missing or promoted to request: %#v", rows)
	}

	bad := event
	bad.At = time.Date(2026, 11, 26, 10, 0, 2, 0, time.UTC).Format(time.RFC3339Nano)
	in.UnreadMessages = []ConversationEvent{bad}
	if got := engine.unreadEvidence(in)[0]["text"]; got != "" {
		t.Fatalf("changed time retained literal: %v", got)
	}
	bad = event
	bad.NativeRecallGeneration = 1
	in.UnreadMessages = []ConversationEvent{bad}
	if got := engine.unreadEvidence(in)[0]["text"]; got != "" {
		t.Fatalf("changed clear generation retained literal: %v", got)
	}
	bad = event
	bad.Relation = "request_before_outgoing"
	in.UnreadMessages = []ConversationEvent{bad}
	if got := engine.unreadEvidence(in)[0]["text"]; got != "" {
		t.Fatalf("direction mismatch retained literal: %v", got)
	}
}

func TestNativeWindowNonTextRemainsMetadata(t *testing.T) {
	store, config, _ := memoryFixture(t)
	engine := &Engine{Store: store, Config: config}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	ref := &SourceRef{Account: "synthetic", Thread: "room@chatroom", Sender: "member", MessageID: "image-1",
		MessageType: 3, OccurredAt: at, Integrity: "observed"}
	in := Input{Channel: "wechat", Account: ref.Account, Thread: ref.Thread, IsGroup: true,
		UnreadMessages: []ConversationEvent{{MessageID: ref.MessageID, Sender: ref.Sender, Text: "[图片] 被描述为猫", At: at,
			Source: ref, NativeWindow: true, Direction: "incoming", Relation: "quoted_message_before_incoming"}}}
	row := engine.unreadEvidence(in)[0]
	if row["text"] != "" || row["message_type"] != 3 || row["evidence_kind"] != "native_media_context" {
		t.Fatalf("media metadata became literal speech: %#v", row)
	}
}

func TestNativeWindowEvidenceUsesMessageTimeBeforeReceiptOrder(t *testing.T) {
	store, config, _ := memoryFixture(t)
	engine := &Engine{Store: store, Config: config}
	event := func(id, text string, second int) ConversationEvent {
		at := time.Date(2026, 11, 26, 10, 0, second, 0, time.UTC).Format(time.RFC3339Nano)
		hash := sha256.Sum256([]byte(text))
		return ConversationEvent{MessageID: id, Sender: "member", Text: text, At: at, NativeWindow: true,
			Direction: "incoming", Relation: "request_after_incoming", Source: &SourceRef{
				Account: "synthetic", Thread: "room@chatroom", Sender: "member", MessageID: id,
				MessageType: 1, OccurredAt: at, BodyHash: hex.EncodeToString(hash[:]), Integrity: "observed"}}
	}
	in := Input{Channel: "wechat", Account: "synthetic", Thread: "room@chatroom", IsGroup: true,
		UnreadMessages: []ConversationEvent{event("later", "后来", 3), event("earlier", "先前", 1)}}
	rows := engine.unreadEvidence(in)
	if len(rows) != 2 || rows[0]["message_id"] != "earlier" || rows[1]["message_id"] != "later" {
		t.Fatalf("receipt order displaced original chronology: %#v", rows)
	}
}
