package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"path/filepath"
	"time"

	"wechat-bot/internal/fishidentity"
	"wechat-bot/internal/stationbrowser"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

const localDouyinNoticePath = "/api/local/apps/douyin/web-notice"

var errLocalDouyinNoticeGrant = errors.New("互动通知授权或网页登录已变化，请重新核对")

type localDouyinNoticeTicket struct {
	value, scope string
	expires      time.Time
}

type localDouyinNoticeInput struct {
	Action        string `json:"action"`
	Revision      uint64 `json:"revision"`
	Ticket        string `json:"ticket,omitempty"`
	AllowMarkRead bool   `json:"allow_mark_read,omitempty"`
}

type localDouyinNoticeItem struct {
	ID        string `json:"id"`
	Type      int    `json:"type"`
	CreatedAt string `json:"created_at,omitempty"`
	WorkID    string `json:"work_id,omitempty"`
}

func validDouyinNoticeAccount(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func (m *localDouyinWebLogin) noticeProfile(fish string, revision uint64) (localDouyinWebRecord, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ctx.Err(); err != nil {
		return localDouyinWebRecord{}, "", err
	}
	dir := filepath.Join(m.home.dataDir, "apps", "douyin", "web-login", fish)
	root, err := workspacefs.OpenPath(dir)
	if err != nil {
		return localDouyinWebRecord{}, "", errLocalDouyinNoticeGrant
	}
	defer root.Close()
	record, err := readLocalDouyinWebRecord(root, fish)
	if err != nil {
		return localDouyinWebRecord{}, "", err
	}
	view := m.view(record)
	if record.Revision != revision || view.Detached || view.Paused || !view.Available || view.State != "saved" || view.AttemptID != "" {
		return localDouyinWebRecord{}, "", errLocalDouyinNoticeGrant
	}
	return record, filepath.Join(dir, record.Profile), nil
}

func (m *localDouyinWebLogin) noticeRoutes(owner *websession.Store, mux *http.ServeMux) {
	mux.HandleFunc("POST "+localDouyinNoticePath, owner.Protect(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		identity, err := m.home.identity.Read()
		if err != nil || identity.LocalOwner == nil || !fishidentity.ValidFishID(identity.FishID) {
			websession.Fail(w, http.StatusConflict, "请先准备本机主人身份")
			return
		}
		session, _, ok := owner.Lookup(r)
		if !ok {
			websession.Fail(w, http.StatusUnauthorized, "请重新登录工作站")
			return
		}
		var input localDouyinNoticeInput
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		if !websession.Decode(w, r, &input) {
			return
		}
		if (input.Action == "bind" || input.Action == "revoke") && (input.AllowMarkRead || input.Ticket != "") {
			websession.Fail(w, http.StatusBadRequest, "账号绑定和撤权使用独立请求")
			return
		}
		if input.Action != "bind" && input.Action != "revoke" && input.Action != "prepare" && input.Action != "read" {
			websession.Fail(w, http.StatusBadRequest, "互动通知操作无效")
			return
		}
		if (input.Action == "prepare" || input.Action == "read") && !input.AllowMarkRead {
			websession.Fail(w, http.StatusForbidden, "请明确确认这次读取可能标记已读")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
		defer cancel()
		stop := context.AfterFunc(m.ctx, cancel)
		defer stop()
		currentOwner := func() error {
			token, _, ok := owner.Lookup(r)
			current, err := m.home.identity.Read()
			if !ok || token != session || err != nil || current.LocalOwner == nil ||
				current.FishID != identity.FishID || current.LocalOwner.ID != identity.LocalOwner.ID {
				return errLocalDouyinNoticeGrant
			}
			return nil
		}
		save := func(account string) {
			if err := currentOwner(); err != nil {
				localDouyinNoticeFailure(w, err)
				return
			}
			view, status, err := m.apply(ctx, identity.FishID, &localDouyinWebInput{
				Action: "set_notice", Revision: input.Revision, noticeAccount: &account,
			})
			if err != nil {
				websession.Fail(w, status, err.Error())
				return
			}
			websession.Respond(w, http.StatusOK, map[string]any{"revision": view.Revision, "notice_bound": view.NoticeBound})
		}
		if input.Action == "revoke" {
			save("")
			return
		}
		record, profile, err := m.noticeProfile(identity.FishID, input.Revision)
		if err != nil {
			localDouyinNoticeFailure(w, err)
			return
		}
		authorize := func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := currentOwner(); err != nil {
				return err
			}
			current, path, err := m.noticeProfile(identity.FishID, input.Revision)
			if err != nil || current != record || path != profile {
				return errLocalDouyinNoticeGrant
			}
			return nil
		}
		if input.Action == "bind" {
			if record.NoticeAccount != "" {
				websession.Fail(w, http.StatusConflict, "请先撤销旧账号绑定，再核对新账号")
				return
			}
			account, err := m.probeNotice(ctx, profile, authorize)
			if err != nil || !validDouyinNoticeAccount(account) {
				localDouyinNoticeFailure(w, err)
				return
			}
			save(account)
			return
		}
		if record.NoticeAccount == "" {
			websession.Fail(w, http.StatusForbidden, "请先绑定互动通知账号")
			return
		}
		scope := localCheckpointKey(identity.FishID, identity.LocalOwner.ID, session, input.Revision, record.NoticeAccount)
		if input.Action == "prepare" {
			if input.Ticket != "" {
				websession.Fail(w, http.StatusBadRequest, "请重新准备本次通知读取")
				return
			}
			value, err := localDouyinWebID()
			if err != nil {
				localDouyinNoticeFailure(w, err)
				return
			}
			m.mu.Lock()
			m.noticeTicket = localDouyinNoticeTicket{value: value, scope: scope, expires: time.Now().Add(time.Minute)}
			m.mu.Unlock()
			websession.Respond(w, http.StatusOK, map[string]string{"ticket": value})
			return
		}
		m.mu.Lock()
		ticket := m.noticeTicket
		usable := input.Ticket != "" && ticket.value == input.Ticket && ticket.scope == scope && time.Now().Before(ticket.expires)
		if usable {
			m.noticeTicket = localDouyinNoticeTicket{}
		}
		m.mu.Unlock()
		if !usable {
			websession.Fail(w, http.StatusConflict, "这次通知读取许可已使用或失效，请重新确认")
			return
		}
		if err := authorize(); err != nil {
			localDouyinNoticeFailure(w, err)
			return
		}
		page, err := m.readNotice(ctx, profile, stationbrowser.DouyinNoticeRequest{Account: record.NoticeAccount, AllowMarkRead: true}, authorize)
		if err != nil {
			localDouyinNoticeFailure(w, err)
			return
		}
		if err := authorize(); err != nil {
			localDouyinNoticeFailure(w, err)
			return
		}
		if page.Account != record.NoticeAccount {
			localDouyinNoticeFailure(w, stationbrowser.ErrDouyinNoticeAccountChanged)
			return
		}
		items := make([]localDouyinNoticeItem, 0, len(page.Items))
		for _, row := range page.Items {
			var created string
			if row.CreatedAt > 0 && row.CreatedAt < 253402300800 {
				created = time.Unix(row.CreatedAt, 0).UTC().Format(time.RFC3339)
			}
			items = append(items, localDouyinNoticeItem{ID: row.ID, Type: row.Type, CreatedAt: created, WorkID: row.WorkID})
		}
		websession.Respond(w, http.StatusOK, map[string]any{"items": items, "has_more": page.HasMore})
	}))
}

func localDouyinNoticeFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errLocalDouyinNoticeGrant),
		errors.Is(err, stationbrowser.ErrDouyinNoticeAccountChanged),
		errors.Is(err, stationbrowser.ErrDouyinLoginActive),
		errors.Is(err, stationbrowser.ErrDouyinWebLoginRequired):
		websession.Fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		websession.Fail(w, http.StatusGatewayTimeout, "互动通知操作超时或已取消")
	default:
		websession.Fail(w, http.StatusServiceUnavailable, "暂时无法核对抖音互动通知")
	}
}
