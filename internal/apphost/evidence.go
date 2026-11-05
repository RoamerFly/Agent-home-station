package apphost

import "context"

// ConnectionRef keeps a platform identifier scoped to a host registration.
type ConnectionRef struct {
	AppID          string `json:"app_id"`
	InstallationID string `json:"installation_id"`
	ConnectionID   string `json:"connection_id"`
	AccountID      string `json:"account_id"`
}

type EventRef struct {
	EventID   string `json:"event_id"`
	MessageID string `json:"message_id"`
	ServerID  string `json:"server_id,omitempty"`
	SenderID  string `json:"sender_id,omitempty"`
	Revision  int    `json:"revision"`
}

type Event struct {
	ConnectionRef
	EventRef
	ConversationID string     `json:"conversation_id"`
	DatasetID      string     `json:"dataset_id"`
	MessageType    int        `json:"message_type,omitempty"`
	OccurredAt     string     `json:"occurred_at,omitempty"`
	ReceivedAt     string     `json:"received_at,omitempty"`
	StoredSequence int64      `json:"stored_sequence"`
	SourceSequence int64      `json:"source_sequence,omitempty"`
	AddresseeIDs   []string   `json:"addressee_ids,omitempty"`
	Quote          *EventRef  `json:"quote,omitempty"`
	Media          []MediaRef `json:"media,omitempty"`
	ReadScope      string     `json:"read_scope"`
}

type MediaRef struct {
	AssetID string `json:"asset_id,omitempty"`
	Kind    string `json:"kind"`
}

type Receipt struct {
	ConnectionRef
	ConversationID string        `json:"conversation_id"`
	IdempotencyKey string        `json:"idempotency_key"`
	OperationID    string        `json:"operation_id,omitempty"`
	Kind           string        `json:"kind"`
	State          string        `json:"state"`
	Lookup         string        `json:"lookup"`
	PlatformState  string        `json:"platform_state,omitempty"`
	PlatformCode   string        `json:"platform_code,omitempty"`
	ServerID       string        `json:"server_id,omitempty"`
	AcceptedParts  []ReceiptPart `json:"accepted_parts,omitempty"`
}

type ReceiptPart struct {
	Index    int    `json:"index"`
	ServerID string `json:"server_id"`
}

// EventReader exposes existing sources through an exact app connection.
type EventReader interface {
	ReadEvent(ctx context.Context, connectionID, conversationID, messageID string) (Event, error)
}

// ReceiptReader may query a previously submitted operation, without resubmitting it.
type ReceiptReader interface {
	ReadReceipt(ctx context.Context, connectionID, key string) (Receipt, error)
}
