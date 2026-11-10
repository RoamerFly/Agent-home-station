package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mediaspec"
)

// AttachmentSource is the private identity of a verified original. Keep its
// field order and JSON names stable for saved asset IDs and document parents.
type AttachmentSource struct {
	Account, Dataset, Thread, Message, Sender, Server, Occurred, BodyHash, Kind, Filename string
	Grant                                                                                 time.Time
	Origin                                                                                string `json:"origin,omitempty"`
	Binding                                                                               string `json:"binding,omitempty"`
}

type AttachmentSourceRequest struct {
	AttachmentRequest
	Grant     time.Time
	Reference *companion.SourceRef
	VideoOnly bool
}

type AttachmentSourceReader interface {
	ChannelsReader
	Account() string
	Message(context.Context, string) (linuxnative.Message, error)
	Identity(context.Context) (string, error)
}

// ResolveAttachmentSource verifies the platform's exact message and dataset.
// The host supplies current grants and the memory/forgetting check. No file
// locator, account credential or download capability enters the result.
func ResolveAttachmentSource(ctx context.Context, reader AttachmentSourceReader, req AttachmentSourceRequest,
	allowed func(context.Context, string) bool, confirm func(context.Context, companion.Input) bool,
) (AttachmentSource, error) {
	thread, message := req.Thread, req.Message
	if reader == nil || reader.Account() == "" || req.Grant.IsZero() || allowed == nil || !allowed(ctx, thread) {
		return AttachmentSource{}, ErrAttachmentDenied
	}
	m, err := reader.Message(ctx, message)
	if err != nil {
		return AttachmentSource{}, err
	}
	if !allowed(ctx, thread) {
		return AttachmentSource{}, ErrAttachmentDenied
	}
	kind, filename := AttachmentKind(m)
	if kind == "" || m.ID != message || m.AccountID != reader.Account() || m.ThreadID != thread || m.Direction != "incoming" || m.SenderID == "" {
		return AttachmentSource{}, ErrAttachmentDenied
	}
	channels := IsChannelsShare(m)
	if req.VideoOnly && kind != "video" || channels && !req.VideoOnly {
		return AttachmentSource{}, ErrAttachmentDenied
	}
	sum := sha256.Sum256([]byte(m.Body))
	bodyHash := hex.EncodeToString(sum[:])
	if r := req.Reference; r != nil {
		occurred, timeErr := time.Parse(time.RFC3339Nano, m.CreatedAt)
		referenced, refTimeErr := time.Parse(time.RFC3339Nano, r.OccurredAt)
		if r.MessageID != message || r.Sender != m.SenderID || r.Account != m.AccountID || r.Thread != m.ThreadID ||
			r.ServerID != m.ServerID || r.MessageType != m.Type || timeErr != nil || refTimeErr != nil || !referenced.Equal(occurred) ||
			r.EventID == "" && r.BodyHash != bodyHash {
			return AttachmentSource{}, ErrAttachmentDenied
		}
		in := companion.Input{Channel: ID, Account: m.AccountID, Thread: m.ThreadID, Sender: m.SenderID, TraceID: m.ID, MessageTime: m.CreatedAt, Source: r}
		if confirm == nil || !confirm(ctx, in) {
			return AttachmentSource{}, ErrAttachmentDenied
		}
	}
	dataset, err := reader.Identity(ctx)
	if err != nil {
		return AttachmentSource{}, err
	}
	if !allowed(ctx, thread) {
		return AttachmentSource{}, ErrAttachmentDenied
	}
	source := AttachmentSource{Account: m.AccountID, Dataset: dataset, Thread: thread, Message: message, Sender: m.SenderID, Server: m.ServerID, Occurred: m.CreatedAt,
		BodyHash: bodyHash, Kind: kind, Filename: filename, Grant: req.Grant}
	if channels {
		channel, err := ReadChannelsSource(ctx, m, reader.Account(), reader)
		if err != nil {
			return AttachmentSource{}, err
		}
		if !allowed(ctx, thread) {
			return AttachmentSource{}, ErrAttachmentDenied
		}
		source.Origin = "channels"
		source.Binding = ChannelsVideoBinding(m, channel)
	}
	return source, nil
}

func AttachmentKind(m linuxnative.Message) (kind, filename string) {
	switch m.Type {
	case 3:
		kind = "image"
	case 34:
		kind = "voice"
	case 43:
		kind = "video"
	case 49:
		if IsChannelsShare(m) {
			return "video", "shared-video.mp4"
		}
		name := m.FileAttachmentName()
		if mediaspec.SafeFilename(name) && name != "." && name != ".." && !strings.ContainsAny(name, ":*?<>|") {
			return "file", name
		}
	}
	if kind == "" {
		return "", ""
	}
	for _, attachment := range m.Attachments {
		if attachment.Kind == kind && mediaspec.SafeFilename(attachment.Filename) && attachment.Filename != "." && attachment.Filename != ".." && !strings.ContainsAny(attachment.Filename, ":*?<>|") {
			return kind, attachment.Filename
		}
	}
	return kind, kind + ".bin"
}

func (source AttachmentSource) AssetID() string {
	raw, _ := json.Marshal(source)
	sum := sha256.Sum256(raw)
	return "wa-" + hex.EncodeToString(sum[:])
}

func ValidAttachmentAssetID(id string) bool {
	raw, err := hex.DecodeString(strings.TrimPrefix(id, "wa-"))
	return strings.HasPrefix(id, "wa-") && err == nil && len(raw) == sha256.Size && strings.ToLower(id) == id
}

func ChannelsVideoBinding(message linuxnative.Message, source linuxnative.ChannelsSource) string {
	if source.ObjectID == "" {
		// Some incoming cards supply only an exact locator. Never reuse those
		// cached bytes if the locator or key changes.
		return attachmentSourceDigest(NativeShareFingerprint(message), source.MediaURL, source.DecodeKey, source.ObjectNonceID)
	}
	return attachmentSourceDigest(NativeShareFingerprint(message), source.ObjectID, source.ObjectNonceID)
}

// Use the same JSON array encoding as the former station-side binding.
func attachmentSourceDigest(values ...any) string {
	raw, _ := json.Marshal(values)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
