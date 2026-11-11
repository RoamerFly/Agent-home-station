package station

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/stationservices"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

const stationWechatOwnerFile = "owner-binding.json"

type stationWechatOwnerBinding struct {
	Sender  string    `json:"sender_id"`
	Dataset string    `json:"dataset"`
	Message string    `json:"message_id"`
	Fish    string    `json:"fish_id"`
	Owner   string    `json:"local_owner_id"`
	BoundAt time.Time `json:"bound_at"`
}

type stationWechatOwnerState struct {
	Account   string                     `json:"account_id"`
	Revision  int                        `json:"revision"`
	Binding   *stationWechatOwnerBinding `json:"binding,omitempty"`
	LastKey   string                     `json:"last_key,omitempty"`
	LastInput string                     `json:"last_input,omitempty"`
}

type stationWechatOwnerInput struct {
	Action       string `json:"action"`
	Revision     int    `json:"revision"`
	Key          string `json:"request_key"`
	Message      string `json:"message_id"`
	Digest       string `json:"digest"`
	SameIdentity bool   `json:"same_identity"`
}

type stationWechatOwnerPreview struct {
	Account  string `json:"account_id"`
	Sender   string `json:"sender_id"`
	Thread   string `json:"thread_id"`
	Message  string `json:"message_id"`
	Digest   string `json:"digest"`
	Revision int    `json:"revision"`
	Dataset  string `json:"-"`
}

func readStationWechatOwner(w *stationWechatWorker, account string) (stationWechatOwnerState, error) {
	state := stationWechatOwnerState{Account: account}
	root, err := workspacefs.OpenPath(w.dir)
	if err != nil {
		return state, err
	}
	defer root.Close()
	raw, err := readLegacyImportFile(root, stationWechatOwnerFile, 16<<10)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil || json.Unmarshal(raw, &state) != nil || state.Account != account || state.Revision < 1 {
		return stationWechatOwnerState{}, apphost.ErrUnavailable
	}
	if b := state.Binding; b != nil {
		if !identity.ValidPathID(b.Sender) || !identity.ValidPathID(b.Dataset) || b.Message == "" || len(b.Message) > 8192 ||
			b.Fish == "" || b.Owner == "" || b.BoundAt.IsZero() || strings.HasSuffix(b.Sender, "@chatroom") || b.Sender == "filehelper" {
			return stationWechatOwnerState{}, apphost.ErrUnavailable
		}
	}
	return state, nil
}

func (w *stationWechatWorker) saveWechatOwner(state stationWechatOwnerState) error {
	root, err := workspacefs.OpenPath(w.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	if info, err := root.Lstat(stationWechatOwnerFile); err == nil && !info.Mode().IsRegular() || err != nil && !errors.Is(err, os.ErrNotExist) {
		return apphost.ErrUnavailable
	}
	temp := ".owner-" + identity.RandomToken()
	defer root.Remove(temp)
	if err := writeLegacyStagedFile(root, temp, state); err != nil {
		return err
	}
	return root.Rename(temp, stationWechatOwnerFile)
}

func (s *stationWechat) wechatOwnerState(id, account string) (stationWechatOwnerState, error) {
	state := stationWechatOwnerState{Account: account}
	worker, err := s.worker(id, false)
	if err != nil || worker == nil {
		return state, err
	}
	worker.ownerMu.Lock()
	defer worker.ownerMu.Unlock()
	return readStationWechatOwner(worker, account)
}

func (in stationWechatOwnerInput) valid() bool {
	if in.Revision < 0 {
		return false
	}
	message := in.Message != "" && len(in.Message) <= 8192
	switch in.Action {
	case "preview", "check":
		return message && in.Key == "" && in.Digest == "" && !in.SameIdentity
	case "bind":
		return message && identity.ValidPathID(in.Key) && legacyMemoryDigestValid(in.Digest) && in.SameIdentity
	case "revoke":
		return in.Message == "" && identity.ValidPathID(in.Key) && in.Digest == "" && !in.SameIdentity
	}
	return false
}

func (s *stationWechat) previewWechatOwner(ctx context.Context, id string, state stationWechatOwnerState, owner stationservices.Owner, message string) (stationWechatOwnerPreview, linuxnative.Message, error) {
	view := stationWechatOwnerPreview{}
	native, err := s.WorkerSender(ctx, id)
	if err != nil || native.Account() != state.Account {
		return view, linuxnative.Message{}, apphost.ErrUnavailable
	}
	daemon, err := native.DaemonStatus(ctx)
	if err != nil || !identity.ValidPathID(daemon.ServerID) {
		return view, linuxnative.Message{}, apphost.ErrUnavailable
	}
	record, err := native.Message(ctx, message)
	_, timeErr := time.Parse(time.RFC3339Nano, record.CreatedAt)
	if err != nil || record.ID != message || record.AccountID != state.Account || record.Direction != "incoming" || record.Type != 1 ||
		!identity.ValidPathID(record.SenderID) || record.SenderID == "filehelper" || strings.HasSuffix(record.SenderID, "@chatroom") ||
		!identity.ValidPathID(record.ThreadID) || timeErr != nil {
		return view, linuxnative.Message{}, apphost.ErrInvalidConnectionPolicy
	}
	if !wechatapp.SameNativeOwnership(ctx, native, daemon) {
		return view, linuxnative.Message{}, apphost.ErrLifecycleConflict
	}
	view = stationWechatOwnerPreview{Account: state.Account, Sender: record.SenderID, Thread: record.ThreadID, Message: record.ID, Revision: state.Revision, Dataset: daemon.ServerID}
	view.Digest = localCheckpointKey("wechat-owner-v1", id, owner, state.Revision, daemon.ServerID, daemon.OwnershipGeneration,
		record.ID, record.AccountID, record.SenderID, record.ThreadID, record.ServerID, record.Type, record.Direction, record.CreatedAt, hexDigest([]byte(record.Body)))
	return view, record, nil
}

func (s *stationWechat) wechatOwnerRoute(session *websession.Store, readOwner stationservices.OwnerReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		id := r.PathValue("connection")
		if !artifactID(id, "wechat-") {
			websession.Fail(w, 404, "微信连接不存在")
			return
		}
		sessionID, _, ok := session.Lookup(r)
		owner, err := readOwner()
		if !ok || err != nil || owner.ID == "" || owner.FishID == "" {
			websession.Fail(w, 401, "请先核对本机主人身份")
			return
		}
		current := func() bool {
			key, _, ok := session.Lookup(r)
			fresh, err := readOwner()
			return ok && key == sessionID && err == nil && fresh == owner && r.Context().Err() == nil
		}
		var in stationWechatOwnerInput
		if r.Method != http.MethodGet {
			if !websession.Decode(w, r, &in) {
				return
			}
			if !in.valid() {
				websession.Fail(w, 400, "请检查本人消息、配置版本及确认项")
				return
			}
		}
		connection, err := s.stationWechatNative.Inspect(r.Context(), id)
		if err != nil && (r.Method == http.MethodGet || in.Action == "revoke") {
			worker, workerErr := s.worker(id, false)
			if workerErr == nil && worker != nil {
				root, openErr := workspacefs.OpenPath(worker.dir)
				if openErr == nil {
					raw, readErr := readLegacyImportFile(root, stationWechatOwnerFile, 16<<10)
					root.Close()
					var saved stationWechatOwnerState
					if readErr == nil && json.Unmarshal(raw, &saved) == nil && wechatapp.ConnectionID(saved.Account) == id {
						connection.AccountID, connection.ID, err = saved.Account, id, nil
					}
				}
			}
		}
		if err != nil || connection.ID != id || wechatapp.ConnectionID(connection.AccountID) != id {
			websession.Fail(w, 404, "微信连接暂不可核对")
			return
		}
		state, err := s.wechatOwnerState(id, connection.AccountID)
		if err != nil {
			websession.Fail(w, 503, "本人绑定记录暂不可读")
			return
		}
		if r.Method == http.MethodGet {
			if !current() {
				websession.Fail(w, 401, "请重新登录")
				return
			}
			state.LastKey, state.LastInput = "", ""
			websession.Respond(w, 200, state)
			return
		}
		request := localCheckpointKey(owner, in)
		if in.Key != "" && in.Key == state.LastKey {
			if state.LastInput != request || !current() {
				websession.Fail(w, 409, "操作编号已被其他请求使用")
				return
			}
			state.LastKey, state.LastInput = "", ""
			websession.Respond(w, 200, state)
			return
		}
		if in.Revision != state.Revision {
			websession.Fail(w, 409, "本人绑定已变化，请重新核对")
			return
		}
		var preview stationWechatOwnerPreview
		var record linuxnative.Message
		if in.Action != "revoke" {
			preview, record, err = s.previewWechatOwner(r.Context(), id, state, owner, in.Message)
			if err != nil {
				websession.Fail(w, 409, "无法核对同一账号下的普通文字消息，请刷新后重试")
				return
			}
		}
		if !current() {
			websession.Fail(w, 401, "请重新登录")
			return
		}
		switch in.Action {
		case "preview":
			websession.Respond(w, 200, preview)
			return
		case "check":
			matched := state.Binding != nil && state.Binding.Fish == owner.FishID && state.Binding.Owner == owner.ID &&
				state.Binding.Sender == record.SenderID && state.Binding.Dataset == preview.Dataset
			fresh, stateErr := s.wechatOwnerState(id, connection.AccountID)
			if stateErr != nil || fresh.Revision != state.Revision || !current() {
				websession.Fail(w, 409, "读取期间本人绑定已变化")
				return
			}
			websession.Respond(w, 200, map[string]any{"is_bound_owner": matched, "sender_id": record.SenderID, "account_id": record.AccountID, "revision": state.Revision})
			return
		case "bind":
			if in.Digest != preview.Digest {
				websession.Fail(w, 409, "消息、账号或绑定版本已变化，请重新预览")
				return
			}
		}
		worker, err := s.worker(id, true)
		if err != nil {
			websession.Fail(w, 503, "本人绑定记录无法保存")
			return
		}
		worker.ownerMu.Lock()
		defer worker.ownerMu.Unlock()
		fresh, err := readStationWechatOwner(worker, connection.AccountID)
		if err == nil && fresh.LastKey == in.Key && fresh.LastInput == request && current() {
			fresh.LastKey, fresh.LastInput = "", ""
			websession.Respond(w, 200, fresh)
			return
		}
		if err != nil || fresh.Revision != state.Revision || !current() {
			websession.Fail(w, 409, "本人绑定已变化，请重新核对")
			return
		}
		fresh.Revision++
		fresh.LastKey, fresh.LastInput = in.Key, request
		fresh.Binding = nil
		if in.Action == "bind" {
			// The digest binds the exact daemon generation observed above.
			latest, _, checkErr := s.previewWechatOwner(r.Context(), id, state, owner, in.Message)
			if checkErr != nil || latest.Digest != preview.Digest || !current() {
				websession.Fail(w, 409, "确认期间消息或连接已变化，请重新预览")
				return
			}
			fresh.Binding = &stationWechatOwnerBinding{Sender: preview.Sender, Dataset: preview.Dataset, Message: preview.Message,
				Fish: owner.FishID, Owner: owner.ID, BoundAt: time.Now().UTC()}
		}
		if err := worker.saveWechatOwner(fresh); err != nil {
			websession.Fail(w, 503, "本人绑定尚未保存")
			return
		}
		fresh.LastKey, fresh.LastInput = "", ""
		websession.Respond(w, 200, fresh)
	}
}
