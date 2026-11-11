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
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

const stationSurfAppsFile = "surf-app-grants.json"

type stationSurfAppScope struct {
	ID      string                              `json:"id"`
	Target  apphost.ObservationTarget           `json:"target"`
	Reads   map[apphost.ObservationField]string `json:"reads"`
	Actions map[string]string                   `json:"actions"`
	Fields  []apphost.ObservationField          `json:"fields"`
}

type stationSurfAppsGrant struct {
	Account                            string `json:"account"`
	Revision                           int    `json:"revision"`
	Owner, Fish, Dataset, Key, Request string
	Scopes                             map[string]stationSurfAppScope `json:"scopes"`
}

type stationSurfAppsInput struct {
	Revision int    `json:"revision"`
	Key      string `json:"key"`
	Scopes   []struct {
		ID      string                     `json:"id"`
		Fields  []apphost.ObservationField `json:"fields"`
		Actions []string                   `json:"actions"`
	} `json:"scopes"`
}

func readSurfAppsGrant(root *os.Root, account string) (stationSurfAppsGrant, error) {
	grant := stationSurfAppsGrant{Account: account, Scopes: map[string]stationSurfAppScope{}}
	raw, err := readLegacyImportFile(root, stationSurfAppsFile, 128<<10)
	if errors.Is(err, os.ErrNotExist) {
		return grant, nil
	}
	if err != nil || json.Unmarshal(raw, &grant) != nil || grant.Account != account || grant.Revision < 1 || len(grant.Scopes) > 100 ||
		len(grant.Scopes) > 0 && (grant.Owner == "" || grant.Fish == "" || grant.Dataset == "") {
		return grant, apphost.ErrObservationForbidden
	}
	for key, scope := range grant.Scopes {
		if key != scope.ID || key != surfAppScopeID(scope.Target) || len(scope.Reads) == 0 || len(scope.Actions) > 2 || len(scope.Fields) == 0 {
			return grant, apphost.ErrObservationForbidden
		}
		for field, version := range scope.Reads {
			if !slices.Contains(scope.Target.Fields, field) || version == "" {
				return grant, apphost.ErrObservationForbidden
			}
		}
		for action, version := range scope.Actions {
			if (action != "like" && action != "reply_comment") || version == "" {
				return grant, apphost.ErrObservationForbidden
			}
		}
	}
	return grant, nil
}

var surfFollowFields = []apphost.ObservationField{apphost.ObservationTitle, apphost.ObservationBody, apphost.ObservationComments, apphost.ObservationPageText, apphost.ObservationAudio, apphost.ObservationVisual, apphost.ObservationPlayback, apphost.ObservationImages, apphost.ObservationPlayerEntry, apphost.ObservationOwnVideo}

func surfAppScopeID(target apphost.ObservationTarget) string {
	return localCheckpointKey("surf-app-scope-v1", target.ConnectionRef, target.ContentID)
}

func (s *stationWechat) surfAppScopes(ctx context.Context) map[string]stationSurfAppScope {
	available := map[string]stationSurfAppScope{}
	if s.localChat == nil || s.localChat.observations == nil {
		return available
	}
	for _, target := range s.localChat.observations.Discover(ctx) {
		entry, ok := s.localChat.applications.Lookup(target.AppID)
		if !ok {
			continue
		}
		provider, ok := entry.Provider.(apphost.ObservationProvider)
		if !ok {
			continue
		}
		scope := stationSurfAppScope{ID: surfAppScopeID(target), Target: target, Reads: map[apphost.ObservationField]string{}, Actions: map[string]string{}}
		scope.Fields = append([]apphost.ObservationField{}, target.Fields...)
		for _, field := range surfFollowFields {
			if !slices.Contains(scope.Fields, field) {
				scope.Fields = append(scope.Fields, field)
			}
		}
		for _, field := range target.Fields {
			grant, err := provider.ObservationGrant(ctx, apphost.ObservationAccess{ConnectionRef: target.ConnectionRef, ContentID: target.ContentID, Field: field})
			if err == nil && grant.Allowed && grant.Version != "" {
				scope.Reads[field] = grant.Version
			}
		}
		_, stable := entry.Provider.(apphost.InteractionResourceProvider)
		if provider, ok := entry.Provider.(apphost.InteractionProvider); ok && stable {
			for _, kind := range []string{"like", "reply_comment"} {
				grant, err := provider.GrantInteraction(ctx, apphost.InteractionDraft{Target: target.ConnectionRef, Kind: kind})
				if err == nil && grant.Allowed && grant.Version != "" {
					scope.Actions[kind] = grant.Version
				}
			}
		}
		if len(scope.Reads) > 0 {
			available[scope.ID] = scope
		}
	}
	return available
}

func (s *stationWechat) checkSurfAppsGrant(ctx context.Context, worker *stationWechatWorker, expected stationSurfAppsGrant) error {
	if len(expected.Scopes) == 0 || s.ownerReader == nil || s.localChat == nil {
		return apphost.ErrObservationForbidden
	}
	owner, err := s.ownerReader()
	if err != nil || owner.ID != expected.Owner || owner.FishID != expected.Fish {
		return apphost.ErrObservationForbidden
	}
	root, err := workspacefs.OpenPath(worker.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	current, err := readSurfAppsGrant(root, expected.Account)
	if err != nil || localCheckpointKey(current) != localCheckpointKey(expected) {
		return apphost.ErrObservationForbidden
	}
	native, err := s.WorkerSender(ctx, worker.connectionID)
	if err != nil || native.Account() != expected.Account {
		return apphost.ErrObservationForbidden
	}
	dataset, err := native.Identity(ctx)
	if err != nil || dataset != expected.Dataset {
		return apphost.ErrObservationForbidden
	}
	for _, scope := range expected.Scopes {
		entry, ok := s.localChat.applications.Lookup(scope.Target.AppID)
		if !ok {
			return apphost.ErrObservationForbidden
		}
		provider, ok := entry.Provider.(apphost.ObservationProvider)
		if !ok {
			return apphost.ErrObservationForbidden
		}
		for field, version := range scope.Reads {
			grant, err := provider.ObservationGrant(ctx, apphost.ObservationAccess{ConnectionRef: scope.Target.ConnectionRef, ContentID: scope.Target.ContentID, Field: field})
			if err != nil || !grant.Allowed || grant.Version != version {
				return apphost.ErrObservationForbidden
			}
		}
		if len(scope.Actions) > 0 {
			provider, ok := entry.Provider.(apphost.InteractionProvider)
			if !ok {
				return apphost.ErrObservationForbidden
			}
			for kind, version := range scope.Actions {
				grant, err := provider.GrantInteraction(ctx, apphost.InteractionDraft{Target: scope.Target.ConnectionRef, Kind: kind})
				if err != nil || !grant.Allowed || grant.Version != version {
					return apphost.ErrObservationForbidden
				}
			}
		}
	}
	return ctx.Err()
}

func (s *stationWechat) activeSurfAppsGrant(ctx context.Context, worker *stationWechatWorker, account string) *stationSurfAppsGrant {
	root, err := workspacefs.OpenPath(worker.dir)
	if err != nil {
		return nil
	}
	defer root.Close()
	grant, err := readSurfAppsGrant(root, account)
	if err != nil || len(grant.Scopes) == 0 || s.checkSurfAppsGrant(ctx, worker, grant) != nil {
		return nil
	}
	return &grant
}

func publicSurfAppScope(scope stationSurfAppScope) map[string]any {
	fields := append([]apphost.ObservationField{}, scope.Fields...)
	slices.Sort(fields)
	actions := make([]string, 0, len(scope.Actions))
	for action := range scope.Actions {
		actions = append(actions, action)
	}
	slices.Sort(actions)
	return map[string]any{"id": scope.ID, "target": scope.Target, "fields": fields, "actions": actions}
}

func (s *stationWechat) surfAppsGrantsRoute(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.ownerReader == nil || s.localChat == nil {
		websession.Fail(w, 503, "应用浏览与互动暂不可用")
		return
	}
	id := r.PathValue("connection")
	if !artifactID(id, "wechat-") {
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
		websession.Fail(w, 409, "请先保存当前连接的冲浪配置")
		return
	}
	root, err := workspacefs.OpenPath(worker.dir)
	if err != nil {
		websession.Fail(w, 503, "应用冲浪授权暂不可读")
		return
	}
	defer root.Close()
	worker.surfAppsMu.Lock()
	defer worker.surfAppsMu.Unlock()
	unlock, err := hostfile.TryLock(filepath.Join(worker.dir, "surf-app-grants.lock"))
	if err != nil {
		websession.Fail(w, 409, "应用冲浪授权正在更新")
		return
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	account, dataset := "", ""
	if native, err := s.WorkerSender(ctx, id); err == nil {
		account = native.Account()
		dataset, _ = native.Identity(ctx)
	} else {
		var header struct {
			Account string `json:"account"`
		}
		raw, err := readLegacyImportFile(root, stationSurfAppsFile, 128<<10)
		if err != nil || json.Unmarshal(raw, &header) != nil || wechatapp.ConnectionID(header.Account) != id {
			websession.Fail(w, 409, "连接离线，暂无可撤销的已存应用冲浪授权")
			return
		}
		account = header.Account
	}
	grant, err := readSurfAppsGrant(root, account)
	if err != nil {
		websession.Fail(w, 503, "已存应用冲浪授权不可核对")
		return
	}
	available := s.surfAppScopes(ctx)
	if r.Method == http.MethodPut {
		var in stationSurfAppsInput
		if !websession.Decode(w, r, &in) {
			return
		}
		if !identity.ValidPathID(in.Key) || len(in.Scopes) > 100 {
			websession.Fail(w, 400, "应用冲浪范围或操作编号无效")
			return
		}
		request := localCheckpointKey(owner, in)
		if grant.Key == in.Key {
			if grant.Request != request {
				websession.Fail(w, 409, "操作编号已用于其他配置")
				return
			}
		} else {
			if grant.Revision != in.Revision {
				websession.Fail(w, 409, "应用冲浪授权已变化，请刷新")
				return
			}
			next := stationSurfAppsGrant{Account: account, Revision: grant.Revision + 1, Owner: owner.ID, Fish: owner.FishID, Dataset: dataset, Key: in.Key, Request: request, Scopes: map[string]stationSurfAppScope{}}
			if len(in.Scopes) > 0 && dataset == "" {
				websession.Fail(w, 409, "请先核对当前原生微信账号")
				return
			}
			for _, selection := range in.Scopes {
				scope, ok := available[selection.ID]
				if !ok || len(selection.Fields) == 0 || len(selection.Actions) > 2 || len(selection.Fields) > len(scope.Fields) {
					websession.Fail(w, 409, "请选择当前已授权的应用来源和读取字段")
					return
				}
				if _, duplicate := next.Scopes[scope.ID]; duplicate {
					websession.Fail(w, 400, "应用来源不能重复")
					return
				}
				chosen := stationSurfAppScope{ID: scope.ID, Target: scope.Target, Reads: map[apphost.ObservationField]string{}, Actions: map[string]string{}}
				chosen.Fields = append([]apphost.ObservationField{}, selection.Fields...)
				for _, field := range selection.Fields {
					if !slices.Contains(scope.Fields, field) {
						websession.Fail(w, 409, "应用读取许可已变化")
						return
					}
					if scope.Reads[field] != "" {
						chosen.Reads[field] = scope.Reads[field]
					}
				}
				if len(chosen.Reads) == 0 {
					websession.Fail(w, 400, "请至少选择一个根入口字段，再按需选择后续字段")
					return
				}
				for _, kind := range selection.Actions {
					if scope.Actions[kind] == "" {
						websession.Fail(w, 409, "此应用当前未授权该互动")
						return
					}
					chosen.Actions[kind] = scope.Actions[kind]
				}
				if chosen.Actions["reply_comment"] != "" && !slices.Contains(chosen.Fields, apphost.ObservationComments) {
					websession.Fail(w, 400, "回复评论需同时允许读取评论")
					return
				}
				next.Scopes[scope.ID] = chosen
			}
			fresh, err := s.ownerReader()
			if err != nil || fresh != owner || ctx.Err() != nil {
				websession.Fail(w, 409, "当前主人或连接已变化")
				return
			}
			raw, _ := json.Marshal(next)
			if len(raw) > 128<<10 {
				websession.Fail(w, 400, "应用冲浪授权超过本机单文件容量，请减少选择")
				return
			}
			temp := ".surf-apps-" + identity.RandomToken()
			defer root.Remove(temp)
			if writeLegacyStagedFile(root, temp, next) != nil || root.Rename(temp, stationSurfAppsFile) != nil {
				websession.Fail(w, 503, "应用冲浪授权未保存")
				return
			}
			grant = next
		}
	}
	selected := []map[string]any{}
	keys := []string{}
	for key := range grant.Scopes {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		selected = append(selected, publicSurfAppScope(grant.Scopes[key]))
	}
	choices := []map[string]any{}
	keys = keys[:0]
	for key := range available {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		choices = append(choices, publicSurfAppScope(available[key]))
	}
	current := len(grant.Scopes) > 0 && s.checkSurfAppsGrant(ctx, worker, grant) == nil
	websession.Respond(w, 200, map[string]any{"revision": grant.Revision, "scopes": selected, "available": choices, "current": current})
}
