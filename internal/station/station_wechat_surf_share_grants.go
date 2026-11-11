package station

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/hostfile"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

const stationSurfShareGrantsFile = "surf-share-grants.json"

type stationSurfShareGrants struct {
	Account              string `json:"account"`
	Revision             int    `json:"revision"`
	Owner, Fish, Dataset string
	Auto                 bool                 `json:"auto"`
	Targets              map[string]time.Time `json:"targets"`
	Sources              map[string]string    `json:"sources"`
	Key, Request         string
}

type stationSurfShareGrantInput struct {
	Revision int      `json:"revision"`
	Key      string   `json:"key"`
	Auto     bool     `json:"auto"`
	Targets  []string `json:"targets"`
	Sources  []string `json:"sources"`
}

func readSurfShareGrants(root *os.Root, account string) (stationSurfShareGrants, error) {
	saved := stationSurfShareGrants{Account: account, Targets: map[string]time.Time{}, Sources: map[string]string{}}
	raw, err := readLegacyImportFile(root, stationSurfShareGrantsFile, 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return saved, nil
	}
	if err != nil || json.Unmarshal(raw, &saved) != nil || saved.Account != account || saved.Revision < 1 ||
		len(saved.Targets) > 100 || len(saved.Sources) > 100 || (len(saved.Targets) == 0) != (len(saved.Sources) == 0) ||
		len(saved.Targets) > 0 && (saved.Owner == "" || saved.Fish == "" || saved.Dataset == "") || saved.Auto && len(saved.Targets) == 0 {
		return stationSurfShareGrants{}, apphost.ErrUnavailable
	}
	for target, stamp := range saved.Targets {
		if !identity.ValidPathID(target) || stamp.IsZero() {
			return stationSurfShareGrants{}, apphost.ErrUnavailable
		}
	}
	for source, fingerprint := range saved.Sources {
		if !identity.ValidPathID(source) || !legacyMemoryDigestValid(fingerprint) {
			return stationSurfShareGrants{}, apphost.ErrUnavailable
		}
	}
	return saved, nil
}

func (s *stationWechat) surfShareAuthority(ctx context.Context, worker *stationWechatWorker, account string) (stationSurfShareGrants, stationWechatPolicy, stationSurfSchedule, error) {
	root, err := workspacefs.OpenPath(worker.dir)
	if err != nil {
		return stationSurfShareGrants{}, stationWechatPolicy{}, stationSurfSchedule{}, err
	}
	defer root.Close()
	grant, err := readSurfShareGrants(root, account)
	if err != nil {
		return grant, stationWechatPolicy{}, stationSurfSchedule{}, err
	}
	policy, err := readStationWechatPolicy(root)
	if err != nil {
		return grant, policy, stationSurfSchedule{}, err
	}
	worker.surfMu.Lock()
	state, err := worker.surfSchedule(account)
	worker.surfMu.Unlock()
	if err != nil {
		return grant, policy, state, err
	}
	if s.ownerReader == nil {
		return grant, policy, state, apphost.ErrObservationForbidden
	}
	owner, err := s.ownerReader()
	if err != nil || grant.Owner != owner.ID || grant.Fish != owner.FishID || ctx.Err() != nil {
		return grant, policy, state, apphost.ErrObservationForbidden
	}
	return grant, policy, state, nil
}

func (s *stationWechat) checkSurfShareGrant(ctx context.Context, worker *stationWechatWorker, native *linuxnative.Sender, expected stationSurfShareGrants, target, source, fingerprint string, automatic bool) error {
	grant, policy, state, err := s.surfShareAuthority(ctx, worker, native.Account())
	if err != nil || localCheckpointKey(grant) != localCheckpointKey(expected) || grant.Targets[target].IsZero() ||
		!grant.Targets[target].Equal(policy.Grants[target]) || grant.Sources[source] != fingerprint ||
		automatic && (!grant.Auto || !state.Enabled || state.Settings.clock().Quiet(time.Now())) {
		return apphost.ErrObservationForbidden
	}
	matched := slices.ContainsFunc(state.Settings.Sources, func(sourceRow mcptools.DiscoverySource) bool {
		return sourceRow.Enabled && sourceRow.ID == source && localCheckpointKey(sourceRow) == fingerprint
	})
	dataset, nativeErr := native.Identity(ctx)
	if !matched || nativeErr != nil || dataset != grant.Dataset {
		return apphost.ErrObservationForbidden
	}
	return ctx.Err()
}

func (s *stationWechat) surfShareGrantsRoute(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("connection")
	if !artifactID(id, "wechat-") || s.ownerReader == nil {
		websession.Fail(w, 404, "微信连接不存在")
		return
	}
	owner, err := s.ownerReader()
	if err != nil {
		websession.Fail(w, 401, "请先登录本机主人")
		return
	}
	worker, err := s.worker(id, false)
	if err != nil || worker == nil {
		websession.Fail(w, 409, "请先保存此连接的冲浪与会话设置")
		return
	}
	root, err := workspacefs.OpenPath(worker.dir)
	if err != nil {
		websession.Fail(w, 503, "冲浪分享授权暂不可读")
		return
	}
	defer root.Close()
	worker.surfShareGrantMu.Lock()
	defer worker.surfShareGrantMu.Unlock()
	unlock, err := hostfile.TryLock(filepath.Join(worker.dir, "surf-share-grants.lock"))
	if err != nil {
		websession.Fail(w, 409, "分享授权正在更新，请稍后重试")
		return
	}
	defer unlock()
	// The saved account allows revocation while the native service is offline.
	account, dataset := "", ""
	if native, nativeErr := s.WorkerSender(r.Context(), id); nativeErr == nil {
		account = native.Account()
		dataset, _ = native.Identity(r.Context())
	} else {
		raw, readErr := readLegacyImportFile(root, stationSurfShareGrantsFile, 64<<10)
		var header struct {
			Account string `json:"account"`
		}
		if readErr != nil || json.Unmarshal(raw, &header) != nil || wechatapp.ConnectionID(header.Account) != id {
			websession.Fail(w, 409, "微信连接离线，暂无可撤销的已存授权")
			return
		}
		account = header.Account
	}
	grant, err := readSurfShareGrants(root, account)
	policy, policyErr := readStationWechatPolicy(root)
	worker.surfMu.Lock()
	state, stateErr := worker.surfSchedule(account)
	worker.surfMu.Unlock()
	if err != nil || policyErr != nil || stateErr != nil {
		websession.Fail(w, 503, "冲浪分享配置暂不可核对")
		return
	}
	targets := []string{}
	for target, stamp := range policy.Grants {
		if !stamp.IsZero() {
			targets = append(targets, target)
		}
	}
	slices.Sort(targets)
	available := map[string]string{}
	for _, source := range state.Settings.Sources {
		if source.Enabled {
			available[source.ID] = localCheckpointKey(source)
		}
	}
	if r.Method == http.MethodPut {
		var in stationSurfShareGrantInput
		if !websession.Decode(w, r, &in) {
			return
		}
		if !identity.ValidPathID(in.Key) || !validWechatCrossThreads(in.Targets) || !validWechatCrossThreads(in.Sources) ||
			(len(in.Targets) == 0) != (len(in.Sources) == 0) || in.Auto && len(in.Targets) == 0 {
			websession.Fail(w, 400, "请选择分享目标和公开来源，或全部清空以撤权")
			return
		}
		request := localCheckpointKey(owner, in)
		if grant.Key == in.Key {
			if grant.Request != request {
				websession.Fail(w, 409, "操作编号已被其他配置使用")
				return
			}
		} else {
			if grant.Revision != in.Revision {
				websession.Fail(w, 409, "授权已变化，请刷新后重试")
				return
			}
			next := stationSurfShareGrants{Account: account, Revision: grant.Revision + 1, Owner: owner.ID, Fish: owner.FishID, Auto: in.Auto,
				Targets: map[string]time.Time{}, Sources: map[string]string{}, Key: in.Key, Request: request}
			if len(in.Targets) > 0 {
				native, err := s.WorkerSender(r.Context(), id)
				if err != nil {
					websession.Fail(w, 409, "请先恢复当前微信连接")
					return
				}
				next.Dataset, err = native.Identity(r.Context())
				if err != nil || next.Dataset == "" {
					websession.Fail(w, 409, "当前原生账号暂不可核对")
					return
				}
				for _, target := range in.Targets {
					if policy.Grants[target].IsZero() {
						websession.Fail(w, 409, "分享目标没有当前回复授权")
						return
					}
					next.Targets[target] = policy.Grants[target]
				}
				for _, source := range in.Sources {
					if available[source] == "" {
						websession.Fail(w, 409, "公开来源未启用")
						return
					}
					next.Sources[source] = available[source]
				}
			}
			fresh, err := s.ownerReader()
			if err != nil || fresh != owner || r.Context().Err() != nil {
				websession.Fail(w, 401, "本机主人已变化")
				return
			}
			temp := ".surf-grants-" + identity.RandomToken()
			defer root.Remove(temp)
			if writeLegacyStagedFile(root, temp, next) != nil || root.Rename(temp, stationSurfShareGrantsFile) != nil {
				websession.Fail(w, 503, "分享授权未保存")
				return
			}
			grant = next
		}
	}
	current := grant.Owner == owner.ID && grant.Fish == owner.FishID && len(grant.Targets) > 0 && dataset != "" && dataset == grant.Dataset
	for target, stamp := range grant.Targets {
		current = current && stamp.Equal(policy.Grants[target])
	}
	for source, fingerprint := range grant.Sources {
		current = current && fingerprint == available[source]
	}
	public := map[string]any{"revision": grant.Revision, "auto": grant.Auto, "targets": grant.Targets, "sources": grant.Sources}
	websession.Respond(w, 200, map[string]any{"grant": public, "current": current, "available_targets": targets, "available_sources": available})
}
