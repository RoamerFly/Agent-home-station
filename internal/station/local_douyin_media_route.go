package station

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/stationbrowser"
	"wechat-bot/internal/websession"
)

const localDouyinMediaResponseLimit = 32 << 20

// This owner-only endpoint hands a verified video body to the co-located
// WeChat service. Browser profile data and signed CDN locations never leave
// the Station. The source video ID comes from a received, resolved link.
func (h *localHomeService) douyinWebMediaRoutes(owner *websession.Store, mux *http.ServeMux) {
	mux.HandleFunc("GET /api/local/apps/douyin/media/{id}", owner.Protect(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		id := r.PathValue("id")
		if !validLocalDouyinMediaID(id) {
			websession.Fail(w, http.StatusBadRequest, "抖音作品编号无效")
			return
		}
		reader := &localDouyinWebObservation{home: h}
		initial, err := reader.state(r.Context())
		if err != nil || !localDouyinMediaAllowed(initial) {
			websession.Fail(w, http.StatusForbidden, "抖音视频读取未启用")
			return
		}
		m := h.douyinWeb
		m.mu.Lock()
		read, service := m.readMedia, m.ctx
		m.mu.Unlock()
		if read == nil || service == nil {
			websession.Fail(w, http.StatusServiceUnavailable, "抖音视频读取服务不可用")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 95*time.Second)
		defer cancel()
		stop := context.AfterFunc(service, cancel)
		defer stop()
		check := func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			current, err := reader.state(ctx)
			if err != nil || !localDouyinMediaAllowed(current) || current.version != initial.version || current.profile != initial.profile || current.connection.AccountID != initial.connection.AccountID {
				return apphost.ErrObservationForbidden
			}
			return nil
		}
		if err := check(); err != nil {
			websession.Fail(w, http.StatusForbidden, "抖音视频读取许可已变化")
			return
		}
		media, err := read(ctx, initial.profile, id, check)
		if err == nil {
			err = check()
		}
		if err != nil {
			status, message := localDouyinMediaFailure(err)
			websession.Fail(w, status, message)
			return
		}
		mime := strings.ToLower(strings.TrimSpace(strings.Split(media.ContentType, ";")[0]))
		if len(media.Data) == 0 || len(media.Data) > localDouyinMediaResponseLimit || !localDouyinMediaSignature(mime, media.Data) {
			websession.Fail(w, http.StatusBadGateway, "抖音视频原件不完整或格式不匹配")
			return
		}
		sum := sha256.Sum256(media.Data)
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Content-Length", strconv.Itoa(len(media.Data)))
		w.Header().Set("X-Content-SHA256", hex.EncodeToString(sum[:]))
		w.Header().Set("Content-Disposition", "attachment; filename=\"douyin-"+id+localDouyinMediaExtension(mime)+"\"")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(media.Data)
	}))
}

func validLocalDouyinMediaID(id string) bool {
	if len(id) < 1 || len(id) > 20 || id[0] == '0' {
		return false
	}
	for i := range id {
		if id[i] < '0' || id[i] > '9' {
			return false
		}
	}
	return true
}

func localDouyinMediaAllowed(a localDouyinWebAccess) bool {
	return a.connection.Configuration == "ready" && a.record.Profile != "" && a.record.ConfirmedAt != "" && !a.record.Paused && !a.record.Detached && a.record.ReadEnabled && localDouyinWebScope(a.record) == "home_and_videos"
}

func localDouyinMediaSignature(mime string, data []byte) bool {
	switch mime {
	case "video/mp4":
		return len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp"))
	case "video/webm":
		return len(data) >= 4 && bytes.Equal(data[:4], []byte{0x1a, 0x45, 0xdf, 0xa3})
	default:
		return false
	}
}

func localDouyinMediaExtension(mime string) string {
	if mime == "video/webm" {
		return ".webm"
	}
	return ".mp4"
}

func localDouyinMediaFailure(err error) (int, string) {
	switch {
	case errors.Is(err, apphost.ErrObservationForbidden):
		return http.StatusForbidden, "抖音视频读取许可已变化"
	case errors.Is(err, stationbrowser.ErrDouyinLoginActive):
		return http.StatusConflict, "抖音登录窗口仍在使用该资料"
	case errors.Is(err, stationbrowser.ErrDouyinWebMediaTooLarge):
		return http.StatusRequestEntityTooLarge, "抖音视频超过当前读取容量"
	case errors.Is(err, stationbrowser.ErrBrowserUnavailable):
		return http.StatusServiceUnavailable, "抖音网页浏览器不可用"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "抖音视频读取超时"
	default:
		return http.StatusBadGateway, "抖音视频原件暂不可读取"
	}
}
