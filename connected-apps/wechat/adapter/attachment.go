package adapter

import (
	"context"
	"encoding/json"
	"errors"
)

// ErrAttachmentDenied is intentionally indistinguishable from a missing
// attachment to callers outside the host.  The host decides why the current
// conversation may not read a source.
var ErrAttachmentDenied = errors.New("attachment source is not authorized")

// ErrAttachmentNotCached means that the source was verified, is an ordinary
// file attachment, and its original has not been retained yet.  It is never a
// signal to download an image, voice note, or arbitrary message.
var ErrAttachmentNotCached = errors.New("attachment original is not cached")

// Attachment is the deliberately small preview returned to an application.
// It contains no native locator, database handle, or private storage path.
type Attachment struct {
	AssetID   string
	MessageID string
	Filename  string
	MIME      string
	Bytes     int64
}

// AttachmentHost keeps authorization, source resolution, native transfer and
// asset registration on the host side.  Implementations must recheck Allowed
// around every operation which can observe or acquire source bytes.
type AttachmentHost interface {
	CurrentThread() string
	Allowed(context.Context, string) bool
	ResolveAttachment(context.Context, string, string) (Attachment, error)
	DownloadAttachment(context.Context, string, string) error
	InvalidAttachment(string) error
}

// ReadAttachment decodes the public tool request and coordinates a narrowly
// scoped host acquisition. The application cannot replace the selected source
// after decoding: the same thread/message pair is used for resolve, download,
// and final resolve. A late policy revocation or cancellation returns no
// preview.
func ReadAttachment(ctx context.Context, raw json.RawMessage, host AttachmentHost) (map[string]any, error) {
	var in struct {
		Message string `json:"message_id"`
		Thread  string `json:"thread_id"`
	}
	if host == nil || json.Unmarshal(raw, &in) != nil || in.Message == "" {
		if host == nil {
			return nil, errors.New("attachment host unavailable")
		}
		return nil, host.InvalidAttachment("invalid attachment request")
	}
	if in.Thread == "" {
		in.Thread = host.CurrentThread()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !host.Allowed(ctx, in.Thread) {
		return nil, ErrAttachmentDenied
	}
	attachment, err := host.ResolveAttachment(ctx, in.Thread, in.Message)
	if errors.Is(err, ErrAttachmentNotCached) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !host.Allowed(ctx, in.Thread) {
			return nil, ErrAttachmentDenied
		}
		if err := host.DownloadAttachment(ctx, in.Thread, in.Message); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !host.Allowed(ctx, in.Thread) {
			return nil, ErrAttachmentDenied
		}
		attachment, err = host.ResolveAttachment(ctx, in.Thread, in.Message)
	}
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !host.Allowed(ctx, in.Thread) {
		return nil, ErrAttachmentDenied
	}
	return map[string]any{
		"asset_id": attachment.AssetID, "message_id": attachment.MessageID,
		"filename": attachment.Filename, "mime": attachment.MIME,
		"bytes": attachment.Bytes, "status": "original_available",
		"instruction": "已取得经核验的原件；可交给当前会话的已授权处理能力，或按用户要求准备发送。不要猜测本机路径、文件内容或投递结果。",
	}, nil
}
