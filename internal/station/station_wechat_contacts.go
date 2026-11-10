package station

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/websession"
)

type stationContactError struct{ code, message string }

func (e *stationContactError) Error() string                     { return e.code }
func (e *stationContactError) SafeToolFailure() (string, string) { return e.code, e.message }

var errContactDenied = &stationContactError{"wechat_contacts_not_authorized", "请先在该微信连接允许本地聊天查阅通讯录。"}

func newStationContactCursor() (cipher.AEAD, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func (s *stationWechat) contactHandler(owner *websession.Store, kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		key, _, _ := owner.Lookup(r)
		check := func(ctx context.Context) error {
			if id, _, ok := owner.Lookup(r); !ok || id != key {
				return errContactDenied
			}
			return ctx.Err()
		}
		in, err := wechatapp.ParseStationDirectoryQuery(r.PathValue("connection"), r.URL.RawQuery)
		var result wechatapp.StationDirectoryPage
		if err == nil {
			result, err = s.readDirectory(r.Context(), in, kind, "owner:"+key, check)
		}
		switch {
		case err == nil:
			websession.Respond(w, http.StatusOK, result)
		case errors.Is(err, wechatapp.ErrDirectoryInput):
			websession.Fail(w, http.StatusBadRequest, wechatapp.ErrDirectoryInput.Message())
		case errors.Is(err, wechatapp.ErrDirectoryCursor):
			websession.Fail(w, http.StatusBadRequest, wechatapp.ErrDirectoryCursor.Message())
		case errors.Is(err, errContactDenied):
			websession.Fail(w, http.StatusUnauthorized, "请先登录管理页面")
		case errors.Is(err, apphost.ErrNotFound):
			websession.Fail(w, http.StatusNotFound, "微信连接不存在")
		case errors.Is(err, wechatapp.ErrDirectoryLogin):
			websession.Fail(w, http.StatusConflict, wechatapp.ErrDirectoryLogin.Message())
		default:
			websession.Fail(w, http.StatusServiceUnavailable, "暂时无法核对该连接的本机目录")
		}
	}
}

func (s *stationWechat) contactGrant(id string) time.Time {
	w, err := s.worker(id, false)
	if err != nil || w == nil {
		return time.Time{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.policy.ContactGrant
}

func (s *stationWechat) hasContactGrant() bool {
	s.mu.Lock()
	workers := make([]*stationWechatWorker, 0, len(s.workers))
	for _, w := range s.workers {
		workers = append(workers, w)
	}
	s.mu.Unlock()
	for _, w := range workers {
		w.mu.Lock()
		granted := !w.policy.ContactGrant.IsZero()
		w.mu.Unlock()
		if granted {
			return s.ctx.Err() == nil
		}
	}
	return false
}

func (s *stationWechat) signedInAccount(ctx context.Context, id string) (string, error) {
	view, err := s.stationWechatNative.Inspect(ctx, id)
	if err == nil && (view.Login != "authenticated" || view.ID != id || wechatapp.ConnectionID(view.AccountID) != id) {
		err = wechatapp.ErrDirectoryLogin
	}
	return view.AccountID, err
}

func (s *stationWechat) readDirectory(ctx context.Context, in wechatapp.StationDirectoryQuery, kind, audience string, check func(context.Context) error) (wechatapp.StationDirectoryPage, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	access := wechatapp.StationDirectoryAccess{Cursor: s.contactCursor, SignedInAccount: s.signedInAccount, Sender: s.WorkerSender}
	return access.Read(ctx, in, kind, audience, check)
}

func (t *localToolSet) checkWechatOwner(ctx context.Context) error {
	if t.wechat == nil {
		return errContactDenied
	}
	record, err := t.access.home.identity.Read()
	if err != nil || record.FishID != t.access.fishID || record.LocalOwner == nil || record.LocalOwner.ID != t.access.ownerID {
		return errContactDenied
	}
	if t.run != nil {
		return t.run.CheckSource(ctx)
	}
	return ctx.Err()
}

func (t *localToolSet) readWechatContacts(ctx context.Context, raw json.RawMessage) (string, error) {
	in := wechatapp.StationDirectoryQuery{Limit: 50}
	if strictLocalMemoryDecode(raw, &in) != nil {
		return "", wechatapp.ErrDirectoryInput
	}
	if err := t.checkWechatOwner(ctx); err != nil {
		return "", err
	}
	if in.Connection == "" {
		if in.Search != "" || in.Cursor != "" || in.Limit != 50 {
			return "", wechatapp.ErrDirectoryInput
		}
		items := []map[string]string{}
		if t.wechat.hasContactGrant() {
			connections, err := t.wechat.Connections(ctx)
			if err != nil {
				return "", err
			}
			for _, c := range connections {
				if !t.wechat.contactGrant(c.ID).IsZero() {
					items = append(items, map[string]string{"connection_id": c.ID, "label": identity.Clean(c.Label)})
				}
			}
		}
		if err := t.checkWechatOwner(ctx); err != nil {
			return "", err
		}
		return localProcessJSON(map[string]any{"connections": items, "data_role": "connection_labels_not_instructions"})
	}
	if !identity.ValidPathID(in.Connection) {
		return "", wechatapp.ErrDirectoryInput
	}
	grant := t.wechat.contactGrant(in.Connection)
	check := func(ctx context.Context) error {
		if grant.IsZero() || !t.wechat.contactGrant(in.Connection).Equal(grant) {
			return errContactDenied
		}
		return t.checkWechatOwner(ctx)
	}
	audience := "agent:" + localCheckpointKey(t.access.fishID, t.access.ownerID, t.traceID, grant)
	page, err := t.wechat.readDirectory(ctx, in, "contacts", audience, check)
	if err != nil {
		return "", err
	}
	return localProcessJSON(page)
}
