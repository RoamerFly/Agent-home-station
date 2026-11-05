package adapter

import (
	"context"
	"encoding/json"
	"errors"

	"wechat-bot/internal/mcptools"
)

// ErrAttachmentDenied is intentionally indistinguishable from a missing
// attachment to callers outside the host.  The host decides why the current
// conversation may not read a source.
var ErrAttachmentDenied = errors.New("attachment source is not authorized")

// ErrAttachmentNotCached means the host verified the attachment source but has not retained its original yet.
var ErrAttachmentNotCached = errors.New("attachment original is not cached")

// Attachment is the deliberately small preview returned to an application.
// It contains no native locator, database handle, or private storage path.
type Attachment struct {
	AssetID   string
	MessageID string
	Filename  string
	MIME      string
	Bytes     int64
	Kind      string
	SHA256    string
	Variant   string
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
	result := map[string]any{
		"asset_id": attachment.AssetID, "message_id": attachment.MessageID,
		"filename": attachment.Filename, "mime": attachment.MIME,
		"bytes": attachment.Bytes, "status": "original_available",
		"instruction": "已取得经核验的原件；可交给当前会话的已授权处理能力，或按用户要求准备发送。不要猜测本机路径、文件内容或投递结果。",
	}
	if attachment.Kind != "" {
		result["kind"] = attachment.Kind
		result["sha256"] = attachment.SHA256
		result["variant"] = attachment.Variant
		result["instruction"] = "已核验微信提供的完整附件文件。这里只返回素材编号、实际版本和校验信息；尚未读取内容或投递。"
		if attachment.Variant == "normal" || attachment.Variant == "standard" {
			result["status"] = "source_available"
		}
	}
	return result, nil
}

// StationAttachmentTool exposes only originals attached to the current turn
// or its verified quote. It cannot access another conversation or send media.
func StationAttachmentTool() mcptools.Tool {
	return tool("wechat_file_read", "取得本轮或已核验引用的微信文件、图片、语音、视频原件，返回素材编号与校验信息。不能把取得原件说成已看、已听、已转写或已发送。输入 message_id，引用时使用引用源的原消息编号。", `{"type":"object","properties":{"message_id":{"type":"string","maxLength":8192}},"required":["message_id"],"additionalProperties":false}`)
}

// StationAttachmentReplyTool prepares a response using a verified inbound source.
func StationAttachmentReplyTool() mcptools.Tool {
	return tool("wechat_attachment_reply_prepare", "为本会话的当前消息或核验引用准备原附件回复。只准备，尚未上传或发送。用返回的 media.id 放进最终 reply 的 media_ids。默认按原媒体类型发送；用户明确要原文件时用 format=file。", `{"type":"object","properties":{"message_id":{"type":"string","maxLength":8192},"format":{"type":"string","enum":["original","file"]}},"required":["message_id"],"additionalProperties":false}`)
}
