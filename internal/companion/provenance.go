package companion

// SourceRef identifies an observed message independently of an execution trace.
// Names are presentation metadata; account, thread and sender IDs are identity.
type SourceRef struct {
	InstanceID  string `json:"instance_id,omitempty"`
	EventID     string `json:"event_id,omitempty"`
	MessageID   string `json:"message_id,omitempty"`
	ServerID    string `json:"server_id,omitempty"`
	Account     string `json:"account,omitempty"`
	Thread      string `json:"thread,omitempty"`
	Sender      string `json:"sender,omitempty"`
	SenderName  string `json:"sender_name,omitempty"`
	MessageType int    `json:"message_type,omitempty"`
	OccurredAt  string `json:"occurred_at,omitempty"`
	ReceivedAt  string `json:"received_at,omitempty"`
	Sequence    int64  `json:"sequence,omitempty"`
	Revision    int    `json:"revision,omitempty"`
	BodyHash    string `json:"body_hash,omitempty"`
	Integrity   string `json:"integrity,omitempty"`
}

// IncomingMediaRef distinguishes the uploader from people or objects depicted.
// Observation is model-produced data, never an instruction or ownership proof.
type IncomingMediaRef struct {
	Source      SourceRef `json:"source"`
	AssetID     string    `json:"asset_id,omitempty"`
	Kind        string    `json:"kind"`
	UploaderID  string    `json:"uploader_id"`
	Observation string    `json:"observation,omitempty"`
	// ObservationExcerpt contains only the host-produced visual/audio evidence.
	// It never includes a share card's title, publisher description, or prompts.
	ObservationExcerpt string `json:"observation_excerpt,omitempty"`
	ObservationState   string `json:"observation_state"`
	ObservationVersion int    `json:"observation_version,omitempty"`
	ObservedRange      string `json:"observed_range,omitempty"`
}

// ReplyContext belongs to one text bubble. Model-proposed IDs must be checked
// against authorized sources before this metadata can authorize a submission.
type ReplyContext struct {
	EvidenceEpoch         string         `json:"evidence_epoch,omitempty"`
	EvidenceAuthorization string         `json:"evidence_authorization,omitempty"`
	SourceVersions        map[string]int `json:"source_versions,omitempty"`
	TargetIDs             []string       `json:"target_ids,omitempty"`
	AnchorMessageID       string         `json:"anchor_message_id,omitempty"`
	AnchorEventID         string         `json:"anchor_event_id,omitempty"`
	SourceEventIDs        []string       `json:"source_event_ids,omitempty"`
	MediaSourceIDs        []string       `json:"media_source_ids,omitempty"`
	ContextVersion        string         `json:"context_version,omitempty"`
	RiskFlags             []string       `json:"risk_flags,omitempty"`
	State                 string         `json:"state,omitempty"`
	QuoteRequired         bool           `json:"quote_required,omitempty"`
}
