package station

import (
	"errors"
	"mime"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/websession"
)

// manualSendInput is intentionally small.  The browser must supply a stable
// request key so a lost response can be looked up without creating a second
// native send.
type manualSendInput struct {
	ThreadID       string `json:"thread_id"`
	Kind           string `json:"kind"`
	Text           string `json:"text"`
	SourceID       string `json:"source_id"`
	RequestKey     string `json:"request_key"`
	QuoteMessageID string `json:"quote_message_id"`
	QuoteServerID  string `json:"quote_server_id"`
}

func (s *stationWechat) manualSendState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("connection")
	if !identity.ValidPathID(id) {
		websession.Fail(w, http.StatusBadRequest, "连接编号无效")
		return
	}
	worker, err := s.worker(id, false)
	if err != nil || worker == nil {
		websession.Fail(w, http.StatusNotFound, "微信连接不存在")
		return
	}
	worker.mu.Lock()
	if worker.policy.Paused {
		worker.mu.Unlock()
		websession.Respond(w, http.StatusOK, map[string]any{"enabled": false, "paused": true, "recipients": []string{}, "kinds": []string{"text", "image", "file", "video", "voice"}})
		return
	}
	seen := make(map[string]struct{}, len(worker.policy.Grants)+len(worker.policy.GroupRules))
	for thread := range worker.policy.Grants {
		seen[thread] = struct{}{}
	}
	for thread := range worker.policy.GroupRules {
		seen[thread] = struct{}{}
	}
	worker.mu.Unlock()
	recipients := make([]string, 0, len(seen))
	for thread := range seen {
		if identity.ValidPathID(thread) {
			recipients = append(recipients, thread)
		}
	}
	sort.Strings(recipients)
	capabilities := linuxnative.Capabilities{}
	if native, nativeErr := s.WorkerSender(r.Context(), id); nativeErr == nil {
		if value, capErr := native.Capabilities(r.Context()); capErr == nil {
			capabilities = value
		}
	}
	websession.Respond(w, http.StatusOK, map[string]any{
		"enabled":              len(recipients) > 0,
		"paused":               false,
		"recipients":           recipients,
		"kinds":                append([]string{"text"}, capabilities.MediaKinds...),
		"media_send_max_bytes": capabilities.MediaSendMaxBytes,
		"quoted_text_send":     capabilities.QuotedTextSend,
	})
}

func (s *stationWechat) manualSend(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("connection")
	var in manualSendInput
	if !websession.Decode(w, r, &in) {
		return
	}
	if !identity.ValidPathID(id) || !identity.ValidPathID(in.ThreadID) || !identity.ValidPathID(in.RequestKey) || len(in.RequestKey) > 128 {
		websession.Fail(w, http.StatusBadRequest, "连接、接收方或操作编号无效")
		return
	}
	if in.Kind == "" {
		in.Kind = "text"
	}
	if in.Kind != "text" && in.Kind != "image" && in.Kind != "file" && in.Kind != "video" && in.Kind != "voice" {
		websession.Fail(w, http.StatusBadRequest, "不支持的消息类型")
		return
	}
	if in.Kind == "text" && (strings.TrimSpace(in.Text) == "" || len(in.Text) > 4096 || !utf8.ValidString(in.Text)) {
		websession.Fail(w, http.StatusBadRequest, "文字内容不能为空且不能超过 4096 字节")
		return
	}
	if in.Kind != "text" && !identity.ValidPathID(in.SourceID) {
		websession.Fail(w, http.StatusBadRequest, "附件来源无效，请先完成上传")
		return
	}
	worker, err := s.worker(id, false)
	if err != nil || worker == nil {
		websession.Fail(w, http.StatusNotFound, "微信连接不存在")
		return
	}
	worker.mu.Lock()
	allowed := !worker.policy.Paused && (worker.policy.Grants[in.ThreadID] != (time.Time{}))
	worker.mu.Unlock()
	if !allowed {
		websession.Fail(w, http.StatusForbidden, "接收方尚未加入当前连接的授权回复范围")
		return
	}
	native, err := s.WorkerSender(r.Context(), id)
	if err != nil {
		websession.Fail(w, http.StatusServiceUnavailable, "微信原生连接不可用，发送未提交")
		return
	}
	if existing, lookupErr := native.LookupSend(r.Context(), in.RequestKey); lookupErr == nil {
		if existing.ThreadID != in.ThreadID {
			websession.Fail(w, http.StatusConflict, "操作编号已绑定到其他接收方")
			return
		}
		websession.Respond(w, http.StatusOK, map[string]any{"operation": existing, "idempotency_key": in.RequestKey, "do_not_auto_resend": true})
		return
	} else if !errors.Is(lookupErr, linuxnative.ErrToolNotFound) {
		websession.Respond(w, http.StatusAccepted, map[string]any{"idempotency_key": in.RequestKey, "state": "unknown", "reason": "发送前状态查询不可用", "do_not_auto_resend": true})
		return
	}
	if err := s.admitPlatformWrite(r.Context(), id, native); err != nil {
		if errors.Is(err, apphost.ErrLifecycleConflict) {
			websession.Fail(w, http.StatusConflict, "当前微信连接正在收尾，发送未提交")
		} else {
			websession.Fail(w, http.StatusForbidden, "当前连接不允许向平台提交发送")
		}
		return
	}
	send := linuxnative.ToolSend{ThreadID: in.ThreadID, Kind: in.Kind, Text: in.Text, SourceID: in.SourceID, IdempotencyKey: in.RequestKey}
	if in.QuoteMessageID != "" || in.QuoteServerID != "" {
		if in.Kind != "text" || in.QuoteMessageID == "" || in.QuoteServerID == "" {
			websession.Fail(w, http.StatusBadRequest, "引用只支持完整的文字消息引用")
			return
		}
		send.Quote = &linuxnative.ToolQuote{MessageID: in.QuoteMessageID, ServerID: in.QuoteServerID}
	}
	status, err := native.Submit(r.Context(), send)
	if err != nil {
		// The native client deliberately hides upstream details.  Preserve the
		// key and make the UI query instead of silently retrying.
		websession.Respond(w, http.StatusAccepted, map[string]any{"idempotency_key": in.RequestKey, "state": "unknown", "reason": "发送结果不明确，请查询本次操作", "do_not_auto_resend": true})
		return
	}
	websession.Respond(w, http.StatusAccepted, map[string]any{"operation": status, "idempotency_key": in.RequestKey, "do_not_auto_resend": true})
}

func (s *stationWechat) manualSendLookup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, operation := r.PathValue("connection"), r.PathValue("operation")
	if !identity.ValidPathID(id) || !identity.ValidPathID(operation) {
		websession.Fail(w, http.StatusBadRequest, "连接或操作编号无效")
		return
	}
	native, err := s.WorkerSender(r.Context(), id)
	if err != nil {
		websession.Respond(w, http.StatusAccepted, map[string]any{"idempotency_key": operation, "state": "unknown", "reason": "微信原生连接不可用", "do_not_auto_resend": true})
		return
	}
	status, err := native.LookupSend(r.Context(), operation)
	if errors.Is(err, linuxnative.ErrToolNotFound) {
		websession.Respond(w, http.StatusOK, map[string]any{"idempotency_key": operation, "state": "unknown", "reason": "原生服务没有返回此操作", "do_not_auto_resend": true})
		return
	}
	if err != nil {
		websession.Respond(w, http.StatusAccepted, map[string]any{"idempotency_key": operation, "state": "unknown", "reason": "状态查询结果不明确", "do_not_auto_resend": true})
		return
	}
	websession.Respond(w, http.StatusOK, map[string]any{"operation": status, "idempotency_key": operation, "do_not_auto_resend": true})
}

func (s *stationWechat) manualSendUpload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("connection")
	if !identity.ValidPathID(id) {
		websession.Fail(w, http.StatusBadRequest, "连接编号无效")
		return
	}
	if err := r.ParseMultipartForm(512 << 20); err != nil {
		websession.Fail(w, http.StatusBadRequest, "附件上传请求无效")
		return
	}
	kind := r.FormValue("kind")
	if kind != "image" && kind != "file" && kind != "video" && kind != "voice" {
		websession.Fail(w, http.StatusBadRequest, "请选择图片、文件、视频或语音类型")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil || header == nil || header.Size <= 0 || header.Size > 512<<20 {
		websession.Fail(w, http.StatusBadRequest, "附件大小无效")
		return
	}
	defer file.Close()
	filename := filepath.Base(header.Filename)
	if filename == "." || filename == "" {
		websession.Fail(w, http.StatusBadRequest, "附件名称无效")
		return
	}
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(filename))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	native, err := s.WorkerSender(r.Context(), id)
	if err != nil {
		websession.Fail(w, http.StatusServiceUnavailable, "微信原生连接不可用，附件未上传")
		return
	}
	if caps, capErr := native.Capabilities(r.Context()); capErr == nil {
		allowed := false
		for _, value := range caps.MediaKinds {
			if value == kind {
				allowed = true
				break
			}
		}
		if !allowed {
			websession.Fail(w, http.StatusBadRequest, "当前微信原生服务不支持此附件类型")
			return
		}
		if limit := caps.MediaSendMaxBytes[kind]; limit > 0 && header.Size > limit {
			websession.Fail(w, http.StatusRequestEntityTooLarge, "附件超过当前微信连接的类型大小限制")
			return
		}
	}
	source, err := native.Upload(r.Context(), filename, contentType, file, header.Size)
	if err != nil {
		websession.Fail(w, http.StatusBadGateway, "附件未能交给微信原生服务")
		return
	}
	websession.Respond(w, http.StatusCreated, source)
}
