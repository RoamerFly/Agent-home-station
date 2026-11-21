package station

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/companion"
)

func (t *stationAttachmentTools) Media(id string) (companion.GeneratedMedia, bool) {
	if asset, ok := strings.CutPrefix(id, "reply-"); ok && strings.HasPrefix(asset, "comfy-") {
		if j, index, allowed := t.comfyMediaAllowed(t.trace, asset); allowed {
			return j.media(index), true
		}
	}
	if asset, ok := strings.CutPrefix(id, "reply-"); ok {
		if j, allowed := t.yueMediaAllowed(t.trace, asset); allowed {
			return j.media(), true
		}
	}
	if asset, ok := strings.CutPrefix(id, "reply-"); ok {
		if j, allowed := t.rvcMediaAllowed(t.trace, asset); allowed {
			return j.media(), true
		}
	}
	if asset, ok := strings.CutPrefix(id, "reply-"); ok {
		if j, allowed := t.externalHTTPMediaAllowed(t.trace, asset); allowed {
			return j.media(), true
		}
	}
	if asset, ok := strings.CutPrefix(id, "reply-"); ok {
		if j, allowed := t.externalTaskMediaAllowed(t.trace, asset); allowed {
			return j.media(), true
		}
	}
	if asset, ok := strings.CutPrefix(id, "reply-"); ok && artifactID(asset, "speech-") {
		p, file, err := t.speechProduct(asset)
		if err != nil {
			return companion.GeneratedMedia{}, false
		}
		file.Close()
		return p.media(), t.speechProductAllowed(t.trace, p)
	}
	if asset, ok := strings.CutPrefix(id, "reply-"); ok && stationDocumentReplyID(asset) {
		r, err := t.readDocumentReply(asset)
		if err != nil {
			return companion.GeneratedMedia{}, false
		}
		_, err = t.documentReplyAccess(context.Background(), t.trace, r)
		return r.media(), err == nil
	}
	item, ok := t.prepared[id]
	return item, ok && t.replyAllowed(t.trace)
}

func (t *stationAttachmentTools) prepareReply(ctx context.Context, raw json.RawMessage) (string, error) {
	var req struct {
		Message string `json:"message_id"`
		Format  string `json:"format"`
	}
	if !t.replyAllowed(t.trace) || strictLocalMemoryDecode(raw, &req) != nil || req.Format != "" && req.Format != "original" && req.Format != "file" {
		return "", wechatapp.ErrAttachmentDenied
	}
	allowed := func() bool { return t.replyAllowed(t.trace) }
	access, err := t.attachmentAccess(t.trace, req.Message, allowed)
	if err != nil {
		return "", err
	}
	request, _ := json.Marshal(wechatapp.AttachmentRequest{Message: req.Message, Thread: t.trace.Thread})
	result, err := wechatapp.ReadAttachment(ctx, request, access)
	if err != nil {
		return "", err
	}
	asset, _ := result["asset_id"].(string)
	kind, _ := result["kind"].(string)
	filename, _ := result["filename"].(string)
	if req.Format == "file" {
		kind = "file"
	}
	if !wechatapp.ValidAttachmentAssetID(asset) || kind == "" || !allowed() {
		return "", wechatapp.ErrAttachmentDenied
	}
	media := companion.GeneratedMedia{ID: "reply-" + asset + "-" + kind, AssetID: asset, Kind: kind, Title: filename}
	if t.prepared == nil {
		t.prepared = make(map[string]companion.GeneratedMedia)
	}
	t.prepared[media.ID] = media
	data, err := json.Marshal(map[string]any{"media": []companion.GeneratedMedia{media}, "status": "prepared"})
	return string(data), err
}

func (t *stationAttachmentTools) stageReply(ctx context.Context, in companion.Input, kind, asset string) (string, error) {
	if strings.HasPrefix(asset, "comfy-") {
		return t.stageComfy(ctx, in, kind, asset)
	}
	if strings.HasPrefix(asset, "yue-") {
		if kind != "file" {
			return "", wechatapp.ErrAttachmentDenied
		}
		return t.stageYuE(ctx, in, asset)
	}
	if strings.HasPrefix(asset, "rvc-") {
		if kind != "file" {
			return "", wechatapp.ErrAttachmentDenied
		}
		return t.stageRVC(ctx, in, asset)
	}
	if strings.HasPrefix(asset, "external-task-") {
		if kind != "file" {
			return "", wechatapp.ErrAttachmentDenied
		}
		return t.stageExternalTask(ctx, in, asset)
	}
	if strings.HasPrefix(asset, "external-mcp-") {
		if kind != "file" {
			return "", wechatapp.ErrAttachmentDenied
		}
		return t.stageExternalMCP(ctx, in, asset)
	}
	if strings.HasPrefix(asset, "external-") {
		if kind != "file" {
			return "", wechatapp.ErrAttachmentDenied
		}
		return t.stageExternalHTTP(ctx, in, asset)
	}
	if artifactID(asset, "speech-") {
		if kind != "file" {
			return "", wechatapp.ErrAttachmentDenied
		}
		return t.stageSpeech(ctx, in, asset)
	}
	if stationDocumentReplyID(asset) {
		if kind != "file" {
			return "", wechatapp.ErrAttachmentDenied
		}
		return t.stageDocumentReply(ctx, in, asset)
	}
	if strings.HasPrefix(asset, "export-") {
		return t.stageWork(ctx, in, kind, asset)
	}
	allowed := func() bool { return t.replyAllowed(in) }
	if !allowed() || !wechatapp.ValidAttachmentAssetID(asset) {
		return "", wechatapp.ErrAttachmentDenied
	}
	for _, ref := range []*companion.SourceRef{in.Source, in.QuotedSource} {
		if !stationAttachmentRef(ref) {
			continue
		}
		access, err := t.attachmentAccess(in, ref.MessageID, allowed)
		if err != nil {
			continue
		}
		source, err := access.source(ctx, in.Thread, ref.MessageID)
		if err != nil || source.AssetID() != asset || kind != source.Kind && kind != "file" {
			continue
		}
		record, original, err := access.open(ctx, source)
		if err != nil {
			return "", err
		}
		defer original.Close()
		return wechatapp.StageMediaUpload(ctx, t.native, wechatapp.MediaUpload{
			Account: source.Account, Filename: source.Filename, MIME: record.MIME, Bytes: record.Bytes, SHA256: record.SHA256,
		}, original, func() bool { return access.Allowed(ctx, source.Thread) })
	}
	return "", errors.New("station_attachment_reply_source_unavailable")
}

func (t *stationAttachmentTools) canSubmitMedia(ctx context.Context, in companion.Input, kind, asset string) bool {
	if strings.HasPrefix(asset, "comfy-") {
		j, index, allowed := t.comfyMediaAllowed(in, asset)
		return allowed && j.Products[index].Kind == kind
	}
	if strings.HasPrefix(asset, "yue-") {
		_, allowed := t.yueMediaAllowed(in, asset)
		return kind == "file" && allowed
	}
	if strings.HasPrefix(asset, "rvc-") {
		_, allowed := t.rvcMediaAllowed(in, asset)
		return kind == "file" && allowed
	}
	if artifactID(asset, "speech-") {
		p, file, err := t.speechProduct(asset)
		if err != nil || kind != "file" {
			if file != nil {
				file.Close()
			}
			return false
		}
		file.Close()
		return t.speechProductAllowed(in, p)
	}
	if !stationDocumentReplyID(asset) {
		return true
	}
	r, err := t.readDocumentReply(asset)
	return kind == "file" && err == nil && t.verifyDocumentReplySource(ctx, in, r) == nil
}
