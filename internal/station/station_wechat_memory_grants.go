package station

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/stationservices"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

const stationMemoryGrantsFile = "memory-grants.json"

type stationMemoryGrants struct {
	Account      string               `json:"account_id"`
	Revision     int                  `json:"revision"`
	Fish         string               `json:"fish_id"`
	Owner        string               `json:"owner_id"`
	StationReads map[string]time.Time `json:"station_reads"`
	LastKey      string               `json:"last_key,omitempty"`
	LastInput    string               `json:"last_input,omitempty"`
}

type stationMemoryGrantInput struct {
	Revision     int      `json:"revision"`
	Key          string   `json:"request_key"`
	StationReads []string `json:"station_reads"`
}

func readStationMemoryGrants(worker *stationWechatWorker, account string) (stationMemoryGrants, error) {
	view := stationMemoryGrants{Account: account, StationReads: map[string]time.Time{}}
	root, err := workspacefs.OpenPath(worker.dir)
	if err != nil {
		return view, err
	}
	defer root.Close()
	raw, err := readLegacyImportFile(root, stationMemoryGrantsFile, 32<<10)
	if errors.Is(err, os.ErrNotExist) {
		return view, nil
	}
	if err != nil || json.Unmarshal(raw, &view) != nil || view.Account != account || view.Revision < 1 || view.Fish == "" || view.Owner == "" || len(view.StationReads) > 100 {
		return stationMemoryGrants{}, apphost.ErrUnavailable
	}
	for thread, stamp := range view.StationReads {
		if !identity.ValidPathID(thread) || stamp.IsZero() {
			return stationMemoryGrants{}, apphost.ErrUnavailable
		}
	}
	return view, nil
}

func (worker *stationWechatWorker) saveMemoryGrants(view stationMemoryGrants) error {
	root, err := workspacefs.OpenPath(worker.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	if info, err := root.Lstat(stationMemoryGrantsFile); err == nil && !info.Mode().IsRegular() || err != nil && !errors.Is(err, os.ErrNotExist) {
		return apphost.ErrUnavailable
	}
	temp := ".memory-grants-" + identity.RandomToken()
	defer root.Remove(temp)
	if err := writeLegacyStagedFile(root, temp, view); err != nil {
		return err
	}
	return root.Rename(temp, stationMemoryGrantsFile)
}

func (s *stationWechat) memoryGrantsRoute(owner *websession.Store, readOwner stationservices.OwnerReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		person, err := readOwner()
		sessionID, _, ok := owner.Lookup(r)
		if err != nil || !ok || person.ID == "" || person.FishID == "" {
			websession.Fail(w, 401, "请先登录本机主人")
			return
		}
		check := func() bool {
			fresh, err := readOwner()
			key, _, ok := owner.Lookup(r)
			return err == nil && fresh == person && ok && key == sessionID && r.Context().Err() == nil
		}
		id := r.PathValue("connection")
		if !artifactID(id, "wechat-") {
			websession.Fail(w, 404, "微信连接不存在")
			return
		}
		connection, err := s.stationWechatNative.Inspect(r.Context(), id)
		if err != nil || connection.ID != id || wechatapp.ConnectionID(connection.AccountID) != id {
			websession.Fail(w, 404, "微信连接暂不可核对")
			return
		}
		var input stationMemoryGrantInput
		if r.Method == http.MethodPut {
			if !websession.Decode(w, r, &input) {
				return
			}
			if input.Revision < 0 || !identity.ValidPathID(input.Key) || len(input.StationReads) > 100 {
				websession.Fail(w, 400, "授权参数无效")
				return
			}
			seen := map[string]bool{}
			for _, thread := range input.StationReads {
				if !identity.ValidPathID(thread) || seen[thread] {
					websession.Fail(w, 400, "请输入不重复的已授权会话编号")
					return
				}
				seen[thread] = true
			}
		}
		worker, err := s.worker(id, r.Method == http.MethodPut)
		if err != nil {
			websession.Fail(w, 503, "会话授权暂不可读")
			return
		}
		view := stationMemoryGrants{Account: connection.AccountID, StationReads: map[string]time.Time{}}
		allowed := []string{}
		if worker != nil {
			// Use the same lock order as worker shutdown and policy mutation.
			worker.mu.Lock()
			defer worker.mu.Unlock()
			worker.ownerMu.Lock()
			defer worker.ownerMu.Unlock()
			view, err = readStationMemoryGrants(worker, connection.AccountID)
			if err != nil {
				websession.Fail(w, 503, "会话授权暂不可读")
				return
			}
			for thread, stamp := range worker.policy.HistoryGrants {
				if !stamp.IsZero() {
					allowed = append(allowed, thread)
				}
			}
			if r.Method == http.MethodPut {
				fingerprint := localCheckpointKey(person, input)
				if view.LastKey == input.Key {
					if view.LastInput != fingerprint || !check() {
						websession.Fail(w, 409, "操作编号已被其他授权请求使用")
						return
					}
					view.LastKey, view.LastInput = "", ""
					websession.Respond(w, 200, view)
					return
				}
				if input.Revision != view.Revision || !check() {
					websession.Fail(w, 409, "授权版本或主人身份已变化")
					return
				}
				next := map[string]time.Time{}
				for _, thread := range input.StationReads {
					history := worker.policy.HistoryGrants[thread]
					if history.IsZero() {
						websession.Fail(w, 409, "请先保存所选会话的历史读取授权")
						return
					}
					next[thread] = history
				}
				view.StationReads = next
				view.Fish, view.Owner = person.FishID, person.ID
				view.Revision++
				view.LastKey, view.LastInput = input.Key, fingerprint
				if err := worker.saveMemoryGrants(view); err != nil {
					websession.Fail(w, 503, "授权尚未保存")
					return
				}
			}
		}
		slices.Sort(allowed)
		if !check() {
			websession.Fail(w, 401, "请重新登录")
			return
		}
		// A changed local identity sees the saved revision and can revoke it,
		// while queries require the exact stored identity.
		view.LastKey, view.LastInput = "", ""
		websession.Respond(w, 200, struct {
			stationMemoryGrants
			Available []string `json:"available_history_threads"`
		}{view, allowed})
	}
}

func (s *stationWechat) memoryGrant(id, account, thread, fish, owner string) (stationMemoryGrants, time.Time, error) {
	worker, err := s.worker(id, false)
	if err != nil || worker == nil || wechatapp.ConnectionID(account) != id {
		return stationMemoryGrants{}, time.Time{}, apphost.ErrUnavailable
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	worker.ownerMu.Lock()
	defer worker.ownerMu.Unlock()
	view, err := readStationMemoryGrants(worker, account)
	stamp := view.StationReads[thread]
	if err != nil || view.Fish != fish || view.Owner != owner || stamp.IsZero() || !stamp.Equal(worker.policy.HistoryGrants[thread]) {
		return view, time.Time{}, apphost.ErrObservationForbidden
	}
	return view, stamp, nil
}

func stationMemoryDirectory(root, id string) (string, error) {
	worker, err := workspacefs.OpenPath(filepath.Join(root, id))
	if err != nil {
		return "", err
	}
	defer worker.Close()
	memory, err := workspacefs.OpenExistingDirectory(worker, "memory")
	if err != nil {
		return "", err
	}
	defer memory.Close()
	file, err := workspacefs.OpenRegular(memory, "memory.sqlite")
	if err != nil {
		return "", err
	}
	file.Close()
	return filepath.Join(root, id, "memory"), nil
}
