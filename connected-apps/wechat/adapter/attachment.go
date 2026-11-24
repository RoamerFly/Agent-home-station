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

// AttachmentRequest identifies the original thread and message to acquire.
type AttachmentRequest struct {
	Message string `json:"message_id"`
	Thread  string `json:"thread_id"`
}

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

// AttachmentHost owns authorization and local staging. Implementations use the
// adapter's source and transfer functions with the current grant before
// registering bytes in the host's private storage.
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
	var in AttachmentRequest
	if host == nil {
		return nil, errors.New("attachment host unavailable")
	}
	if json.Unmarshal(raw, &in) != nil {
		return nil, host.InvalidAttachment("invalid attachment request")
	}
	return AcquireAttachment(ctx, in, host)
}

// AcquireAttachment accepts an already parsed request from the owner API.
func AcquireAttachment(ctx context.Context, in AttachmentRequest, host AttachmentHost) (map[string]any, error) {
	if host == nil {
		return nil, errors.New("attachment host unavailable")
	}
	if in.Message == "" {
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
	return tool("wechat_file_read", "按原 message_id 取得本轮或引用的微信文件、图片、语音、视频原件，返回素材编号和校验信息。", `{"type":"object","properties":{"message_id":{"type":"string","maxLength":8192}},"required":["message_id"],"additionalProperties":false}`)
}

// StationAttachmentReplyTool prepares a response using a verified inbound source.
func StationVoiceTranscriptionTool() mcptools.Tool {
	return tool("wechat_voice_transcribe", "按原 message_id 转写微信语音或 MP3/WAV/FLAC 附件；起点、时长、超时可调，返回文字、实际覆盖和 next_start_seconds。", `{"type":"object","properties":{"message_id":{"type":"string","maxLength":8192},"start_seconds":{"type":"number","minimum":0,"default":0},"duration_seconds":{"type":"number","exclusiveMinimum":0,"default":90},"timeout_seconds":{"type":"integer","minimum":0,"default":180}},"required":["message_id"],"additionalProperties":false}`)
}

func StationImageObservationTool() mcptools.Tool {
	return tool("wechat_image_observe", "按 message_id 观察本轮或引用的微信原图；可填 question 指定细节，analysis_tokens 调整分析长度。", `{"type":"object","properties":{"message_id":{"type":"string","maxLength":8192},"question":{"type":"string","maxLength":4096},"analysis_tokens":{"type":"integer","minimum":0}},"required":["message_id"],"additionalProperties":false}`)
}

func StationDocumentInspectionTool() mcptools.Tool {
	return tool("wechat_document_inspect", "按原 message_id 读取本轮、引用或已查到的历史 DOCX/XLSX/PDF。DOCX/XLSX 按 next_text_offset 续读；PDF 按 next_page 或 next_text_offset 续读，扫描页可用 OCR。续读填返回的 sha256。", `{"type":"object","properties":{"message_id":{"type":"string","minLength":1,"maxLength":8192},"page":{"type":"integer","minimum":1},"ocr_mode":{"type":"string","enum":["auto","off","always"],"default":"auto"},"ocr_dpi":{"type":"integer","minimum":96,"maximum":300,"default":220},"text_offset":{"type":"integer","minimum":0,"default":0},"text_limit":{"type":"integer","minimum":1,"maximum":100000,"default":4000},"expected_sha256":{"type":"string","pattern":"^[a-f0-9]{64}$"}},"required":["message_id"],"additionalProperties":false}`)
}

func StationDocumentPDFReplyTool() mcptools.Tool {
	return tool("wechat_document_export_pdf", "按原 message_id 将 DOCX 导出 PDF 并准备回复；需要交付时把返回的 media.id 放进最终 media_ids。", `{"type":"object","properties":{"message_id":{"type":"string","minLength":1,"maxLength":8192},"filename":{"type":"string","minLength":1,"maxLength":120,"description":"可选的 PDF 交付文件名，包含 .pdf 后缀。"},"expected_sha256":{"type":"string","pattern":"^[a-f0-9]{64}$"}},"required":["message_id"],"additionalProperties":false}`)
}

func StationImageDocumentTool() mcptools.Tool {
	return tool("wechat_document_reconstruct_image", "按原 message_id 将 PNG/JPEG 用 OCR 重建为可编辑 Word，按 format 准备 PDF、DOCX 或两者。可指定 illustration_regions 保留插图；需要交付时把返回的 media.id 放入最终 media_ids。", `{"type":"object","properties":{"message_id":{"type":"string","minLength":1,"maxLength":8192},"base_name":{"type":"string","minLength":1,"maxLength":110,"description":"可选文件名主体，不含后缀。"},"format":{"type":"string","enum":["pdf","docx","both"],"default":"pdf"},"expected_sha256":{"type":"string","pattern":"^[a-f0-9]{64}$"},"illustration_regions":{"type":"array","maxItems":20,"items":{"type":"object","properties":{"left":{"type":"integer","minimum":0},"top":{"type":"integer","minimum":0},"right":{"type":"integer","minimum":1},"bottom":{"type":"integer","minimum":1}},"required":["left","top","right","bottom"],"additionalProperties":false}}},"required":["message_id"],"additionalProperties":false}`)
}

func StationDocumentRevisionTool() mcptools.Tool {
	return tool("wechat_document_revision", "修改本会话此前的可编辑 Word。list 查原稿；resize_title 填 asset_id、expected_sha256 和字号，返回新版 media.id 供交付。", `{"type":"object","properties":{"action":{"type":"string","enum":["list","resize_title"]},"asset_id":{"type":"string"},"expected_sha256":{"type":"string","pattern":"^[a-f0-9]{64}$"},"font_size_pt":{"type":"number","minimum":8,"maximum":48,"multipleOf":0.5},"format":{"type":"string","enum":["pdf","docx","both"]},"filename_contains":{"type":"string","maxLength":120},"after":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":100,"default":10}},"required":["action"],"additionalProperties":false}`)
}

func StationAttachmentReplyTool() mcptools.Tool {
	return tool("wechat_attachment_reply_prepare", "按 message_id 准备当前或引用附件的回复，返回 media.id；放入最终 media_ids 才发送。原文件格式用 format=file。", `{"type":"object","properties":{"message_id":{"type":"string","maxLength":8192},"format":{"type":"string","enum":["original","file"]}},"required":["message_id"],"additionalProperties":false}`)
}

func StationWorkReplyTool() mcptools.Tool {
	return tool("wechat_work_reply", "查看并准备当前会话获准交付的作品。list 返回 delivery_id 和 next_after；prepare 用 delivery_id，最终回复填返回的 media.id。", `{"type":"object","properties":{"action":{"type":"string","enum":["list","prepare"]},"delivery_id":{"type":"string"},"after":{"type":"string"},"limit":{"type":"integer","minimum":1,"default":20}},"required":["action"],"additionalProperties":false}`)
}
