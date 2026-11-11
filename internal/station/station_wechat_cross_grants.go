package station

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/stationservices"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

const stationWechatCrossGrantsFile = "wechat-cross-memory-grants.json"
const stationWechatToStationGrantsFile = "wechat-to-station-memory-grants.json"

// This direction is independent of StationReads. Authority is tied to the
// current native owner binding and the exact underlying policy grant versions.
type stationWechatCrossGrants struct {
	Account       string               `json:"account_id"`
	Revision      int                  `json:"revision"`
	Fish          string               `json:"fish_id"`
	Owner         string               `json:"owner_id"`
	OwnerRevision int                  `json:"owner_revision"`
	Sources       map[string]time.Time `json:"sources"`
	Origins       map[string]time.Time `json:"origins"`
	ActivatedAt   time.Time            `json:"activated_at,omitzero"`
	LastKey       string               `json:"last_key,omitempty"`
	LastInput     string               `json:"last_input,omitempty"`
}

type stationWechatCrossGrantInput struct {
	Revision      int      `json:"revision"`
	OwnerRevision int      `json:"owner_revision"`
	Key           string   `json:"request_key"`
	Sources       []string `json:"sources"`
	Origins       []string `json:"origins"`
}

func emptyWechatCrossGrants(account string) stationWechatCrossGrants {
	return stationWechatCrossGrants{Account: account, Sources: map[string]time.Time{}, Origins: map[string]time.Time{}}
}

func readWechatDirectionGrants(worker *stationWechatWorker, account, filename string) (stationWechatCrossGrants, error) {
	view := emptyWechatCrossGrants(account)
	root, err := workspacefs.OpenPath(worker.dir)
	if err != nil {
		return view, err
	}
	defer root.Close()
	raw, err := readLegacyImportFile(root, filename, 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return view, nil
	}
	if err != nil || json.Unmarshal(raw, &view) != nil || view.Account != account || view.Revision < 1 || view.Fish == "" || view.Owner == "" || view.OwnerRevision < 0 || len(view.Sources) > 100 || len(view.Origins) > 100 {
		return stationWechatCrossGrants{}, apphost.ErrUnavailable
	}
	for _, entries := range []map[string]time.Time{view.Sources, view.Origins} {
		for thread, stamp := range entries {
			if !identity.ValidPathID(thread) || stamp.IsZero() {
				return stationWechatCrossGrants{}, apphost.ErrUnavailable
			}
		}
	}
	if (len(view.Sources) > 0 || len(view.Origins) > 0) && (view.OwnerRevision == 0 || view.ActivatedAt.IsZero()) {
		return stationWechatCrossGrants{}, apphost.ErrUnavailable
	}
	if filename == stationWechatToStationGrantsFile && len(view.Sources) != 0 {
		return stationWechatCrossGrants{}, apphost.ErrUnavailable
	}
	return view, nil
}

func (worker *stationWechatWorker) saveWechatDirectionGrants(view stationWechatCrossGrants, filename string) error {
	root, err := workspacefs.OpenPath(worker.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	if info, err := root.Lstat(filename); err == nil && !info.Mode().IsRegular() || err != nil && !errors.Is(err, os.ErrNotExist) {
		return apphost.ErrUnavailable
	}
	temp := ".cross-memory-" + identity.RandomToken()
	defer root.Remove(temp)
	if err := writeLegacyStagedFile(root, temp, view); err != nil {
		return err
	}
	return root.Rename(temp, filename)
}

func validWechatCrossThreads(threads []string) bool {
	if len(threads) > 100 {
		return false
	}
	seen := map[string]bool{}
	for _, thread := range threads {
		if !identity.ValidPathID(thread) || seen[thread] {
			return false
		}
		seen[thread] = true
	}
	return true
}

func (s *stationWechat) crossMemoryGrantsRoute(session *websession.Store, readOwner stationservices.OwnerReader) http.HandlerFunc {
	return s.wechatMemoryDirectionRoute(session, readOwner, false)
}

func (s *stationWechat) wechatMemoryDirectionRoute(session *websession.Store, readOwner stationservices.OwnerReader, stationSource bool) http.HandlerFunc {
	filename := stationWechatCrossGrantsFile
	if stationSource {
		filename = stationWechatToStationGrantsFile
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		person, err := readOwner()
		key, _, ok := session.Lookup(r)
		if err != nil || !ok || person.ID == "" || person.FishID == "" {
			websession.Fail(w, 401, "请先登录本机主人")
			return
		}
		current := func() bool {
			fresh, err := readOwner()
			next, _, ok := session.Lookup(r)
			return err == nil && fresh == person && ok && key == next && r.Context().Err() == nil
		}
		id := r.PathValue("connection")
		if !artifactID(id, "wechat-") {
			websession.Fail(w, 404, "微信连接不存在")
			return
		}
		var input stationWechatCrossGrantInput
		if r.Method == http.MethodPut {
			if !websession.Decode(w, r, &input) {
				return
			}
			invalidScope := stationSource && len(input.Sources) != 0 || !stationSource && (len(input.Sources) == 0) != (len(input.Origins) == 0)
			if input.Revision < 0 || input.OwnerRevision < 0 || !identity.ValidPathID(input.Key) || !validWechatCrossThreads(input.Sources) || !validWechatCrossThreads(input.Origins) || invalidScope {
				websession.Fail(w, 400, "请选择发起会话和来源会话，或全部清空以撤权")
				return
			}
		}
		connection, err := s.stationWechatNative.Inspect(r.Context(), id)
		if err != nil || connection.ID != id || wechatapp.ConnectionID(connection.AccountID) != id {
			// Saved authority must remain revocable during a native outage. The
			// private file is read under the worker lock below and never queried.
			worker, workerErr := s.worker(id, false)
			if workerErr != nil || worker == nil {
				websession.Fail(w, 404, "微信连接暂不可核对")
				return
			}
			// The validated owner-binding account is the offline connection key.
			worker.ownerMu.Lock()
			root, openErr := workspacefs.OpenPath(worker.dir)
			var state stationWechatOwnerState
			if openErr == nil {
				raw, readErr := readLegacyImportFile(root, stationWechatOwnerFile, 16<<10)
				root.Close()
				if readErr != nil || json.Unmarshal(raw, &state) != nil {
					openErr = apphost.ErrUnavailable
				}
			}
			worker.ownerMu.Unlock()
			if openErr != nil || wechatapp.ConnectionID(state.Account) != id || r.Method == http.MethodPut && len(input.Origins) > 0 {
				websession.Fail(w, 503, "连接离线，仅可查看或撤销已存范围")
				return
			}
			connection.AccountID = state.Account
		}
		liveDataset := ""
		if r.Method == http.MethodPut && len(input.Origins) > 0 {
			native, nativeErr := s.WorkerSender(r.Context(), id)
			if nativeErr != nil || native.Account() != connection.AccountID {
				websession.Fail(w, 503, "请先恢复当前原生微信连接")
				return
			}
			daemon, daemonErr := native.DaemonStatus(r.Context())
			if daemonErr != nil || !wechatapp.SameNativeOwnership(r.Context(), native, daemon) {
				websession.Fail(w, 503, "原生微信数据暂不可核对")
				return
			}
			liveDataset = daemon.ServerID
		}
		worker, err := s.worker(id, r.Method == http.MethodPut)
		if err != nil {
			websession.Fail(w, 503, "跨群授权暂不可读")
			return
		}
		view := emptyWechatCrossGrants(connection.AccountID)
		binding := stationWechatOwnerState{Account: connection.AccountID}
		sources, origins := []string{}, []string{}
		privateAllowed := false
		validBinding := false
		scopesCurrent := false
		if worker != nil {
			worker.mu.Lock()
			defer worker.mu.Unlock()
			worker.ownerMu.Lock()
			defer worker.ownerMu.Unlock()
			view, err = readWechatDirectionGrants(worker, connection.AccountID, filename)
			if err != nil {
				websession.Fail(w, 503, "跨群授权暂不可读")
				return
			}
			binding, err = readStationWechatOwner(worker, connection.AccountID)
			if err != nil {
				websession.Fail(w, 503, "本人绑定暂不可读")
				return
			}
			validBinding = binding.Binding != nil && binding.Binding.Fish == person.FishID && binding.Binding.Owner == person.ID
			if !stationSource {
				for thread, stamp := range worker.policy.HistoryGrants {
					if !stamp.IsZero() {
						sources = append(sources, thread)
					}
				}
			}
			for thread, stamp := range worker.policy.Grants {
				if !stamp.IsZero() {
					origins = append(origins, thread)
				}
			}
			if validBinding {
				privateAllowed = !worker.policy.Grants[binding.Binding.Sender].IsZero()
			}
			if r.Method == http.MethodPut {
				fingerprint := localCheckpointKey(person, input)
				if view.LastKey == input.Key {
					if view.LastInput != fingerprint || !current() {
						websession.Fail(w, 409, "操作编号已被其他请求使用")
						return
					}
				} else {
					if view.Revision != input.Revision || !current() {
						websession.Fail(w, 409, "跨群授权版本或主人身份已变化")
						return
					}
					nextSources, nextOrigins := map[string]time.Time{}, map[string]time.Time{}
					if len(input.Origins) > 0 {
						if !validBinding || input.OwnerRevision != binding.Revision || liveDataset != binding.Binding.Dataset {
							websession.Fail(w, 409, "请先核对当前微信本人绑定")
							return
						}
						for _, thread := range input.Sources {
							stamp := worker.policy.HistoryGrants[thread]
							if stamp.IsZero() {
								websession.Fail(w, 409, "来源会话没有当前历史读取授权")
								return
							}
							nextSources[thread] = stamp
						}
						for _, thread := range input.Origins {
							stamp := worker.policy.Grants[thread]
							if stamp.IsZero() {
								websession.Fail(w, 409, "发起会话没有当前回复授权")
								return
							}
							nextOrigins[thread] = stamp
						}
					}
					view.Sources, view.Origins = nextSources, nextOrigins
					view.Fish, view.Owner = person.FishID, person.ID
					view.OwnerRevision = binding.Revision
					view.ActivatedAt = time.Now().UTC()
					view.Revision++
					view.LastKey, view.LastInput = input.Key, fingerprint
					if err := worker.saveWechatDirectionGrants(view, filename); err != nil {
						websession.Fail(w, 503, "跨群授权尚未保存")
						return
					}
				}
			}
			scopesCurrent = validBinding && view.OwnerRevision == binding.Revision && view.Fish == person.FishID && view.Owner == person.ID && (stationSource || len(view.Sources) > 0) && len(view.Origins) > 0
			for thread, stamp := range view.Sources {
				scopesCurrent = scopesCurrent && stamp.Equal(worker.policy.HistoryGrants[thread])
			}
			for thread, stamp := range view.Origins {
				scopesCurrent = scopesCurrent && stamp.Equal(worker.policy.Grants[thread])
			}
		}
		if !current() {
			websession.Fail(w, 401, "请重新登录")
			return
		}
		slices.Sort(sources)
		slices.Sort(origins)
		view.LastKey, view.LastInput = "", ""
		websession.Respond(w, 200, struct {
			stationWechatCrossGrants
			BindingRevision  int      `json:"current_owner_revision"`
			BindingReady     bool     `json:"owner_bound"`
			PrivateAllowed   bool     `json:"private_reply_authorized"`
			ScopesCurrent    bool     `json:"scopes_current"`
			AvailableSources []string `json:"available_sources"`
			AvailableOrigins []string `json:"available_origins"`
		}{view, binding.Revision, validBinding, privateAllowed, scopesCurrent, sources, origins})
	}
}

type stationWechatCrossAccess struct {
	Digest        string
	PrivateTarget string
	GrantRevision int
}

// Runtime checks must not wait for worker.mu: shutdown drains the actor while
// holding that lock. Policy files are atomically replaced and validated here.
func (worker *stationWechatWorker) crossAuthority(account string) (stationWechatCrossGrants, stationWechatOwnerState, stationWechatPolicy, error) {
	return worker.wechatDirectionAuthority(account, false)
}

func (worker *stationWechatWorker) wechatDirectionAuthority(account string, stationSource bool) (stationWechatCrossGrants, stationWechatOwnerState, stationWechatPolicy, error) {
	worker.ownerMu.Lock()
	defer worker.ownerMu.Unlock()
	filename := stationWechatCrossGrantsFile
	if stationSource {
		filename = stationWechatToStationGrantsFile
	}
	grants, err := readWechatDirectionGrants(worker, account, filename)
	if err != nil {
		return grants, stationWechatOwnerState{}, stationWechatPolicy{}, err
	}
	binding, err := readStationWechatOwner(worker, account)
	if err != nil {
		return grants, binding, stationWechatPolicy{}, err
	}
	root, err := workspacefs.OpenPath(worker.dir)
	if err != nil {
		return grants, binding, stationWechatPolicy{}, err
	}
	defer root.Close()
	policy, err := readStationWechatPolicy(root)
	return grants, binding, policy, err
}

// No cross-conversation content may be opened until this host-owned check
// succeeds. Repeating it before reads and delivery detects revoked authority,
// replacement native data, edited requests, and changed reply destinations.
func (s *stationWechat) verifyWechatCrossRequest(ctx context.Context, id string, in companion.Input, source string, owner stationservices.Owner) (stationWechatCrossAccess, error) {
	return s.verifyWechatCrossScope(ctx, id, in, source, owner, false)
}

func (s *stationWechat) verifyWechatCrossScope(ctx context.Context, id string, in companion.Input, source string, owner stationservices.Owner, forward bool) (stationWechatCrossAccess, error) {
	return s.verifyWechatMemoryDirection(ctx, id, in, source, owner, forward, false)
}

func (s *stationWechat) verifyWechatMemoryDirection(ctx context.Context, id string, in companion.Input, source string, owner stationservices.Owner, forward, stationSource bool) (stationWechatCrossAccess, error) {
	deny := func() (stationWechatCrossAccess, error) {
		return stationWechatCrossAccess{}, apphost.ErrObservationForbidden
	}
	if ctx.Err() != nil || in.Channel != "wechat" || in.Mode != "direct" || !in.Directed || wechatapp.ConnectionID(in.Account) != id || in.Thread == source || in.Preview || in.Surf || in.ReportOnly || in.HistoricalReview || in.BackgroundResult || in.ConversationDelivery || in.SourceOnlyContext || in.OriginTraceID != "" || in.TaskID != "" || in.ReplyContext != nil || in.OriginalText == nil || in.TraceID == "" || in.IsGroup != strings.HasSuffix(in.Thread, "@chatroom") {
		return deny()
	}
	worker, err := s.worker(id, false)
	if err != nil || worker == nil {
		return deny()
	}
	grants, binding, policy, grantErr := worker.wechatDirectionAuthority(in.Account, stationSource)
	sourceGrant, originGrant, privateGrant := policy.HistoryGrants[source], policy.Grants[in.Thread], time.Time{}
	if binding.Binding != nil && !forward {
		privateGrant = policy.Grants[binding.Binding.Sender]
	}
	sourceFile, targetFile := policy.AttachmentGrants[source], policy.MediaReplyGrants[in.Thread]
	b := binding.Binding
	allowedSource := stationSource && !forward && source == "" || !stationSource && !sourceGrant.IsZero() && sourceGrant.Equal(grants.Sources[source])
	if grantErr != nil || b == nil || owner.ID == "" || owner.FishID == "" || grants.Owner != owner.ID || grants.Fish != owner.FishID || b.Owner != owner.ID || b.Fish != owner.FishID || grants.OwnerRevision != binding.Revision || b.Sender != in.Sender || !allowedSource || originGrant.IsZero() || !originGrant.Equal(grants.Origins[in.Thread]) || !forward && in.IsGroup && privateGrant.IsZero() || forward && (!in.IsGroup || sourceFile.IsZero() || targetFile.IsZero()) {
		return deny()
	}
	preview, record, err := s.previewWechatOwner(ctx, id, binding, owner, in.TraceID)
	when, timeErr := time.Parse(time.RFC3339Nano, in.MessageTime)
	actual, actualErr := time.Parse(time.RFC3339Nano, record.CreatedAt)
	if err != nil || timeErr != nil || actualErr != nil || !when.Equal(actual) || when.Before(grants.ActivatedAt) || when.Before(originGrant) || preview.Dataset != b.Dataset || record.SenderID != b.Sender || record.ThreadID != in.Thread || record.Body != *in.OriginalText || !forward && in.IsGroup && when.Before(privateGrant) || !in.IsGroup && in.Thread != b.Sender || forward && (when.Before(sourceFile) || when.Before(targetFile)) {
		return deny()
	}
	if ref := in.Source; ref != nil {
		sourceTime, timeErr := time.Parse(time.RFC3339Nano, ref.OccurredAt)
		if ref.MessageID != in.TraceID || ref.ServerID != record.ServerID || ref.Account != "" && ref.Account != in.Account || ref.Thread != "" && ref.Thread != in.Thread || ref.Sender != "" && ref.Sender != in.Sender || timeErr != nil || !sourceTime.Equal(when) {
			return deny()
		}
	}
	fresh, freshBinding, current, readErr := worker.wechatDirectionAuthority(in.Account, stationSource)
	unchanged := readErr == nil && fresh.Revision == grants.Revision && freshBinding.Revision == binding.Revision && sourceGrant.Equal(current.HistoryGrants[source]) && originGrant.Equal(current.Grants[in.Thread])
	if forward {
		unchanged = unchanged && sourceFile.Equal(current.AttachmentGrants[source]) && targetFile.Equal(current.MediaReplyGrants[in.Thread])
	} else {
		unchanged = unchanged && privateGrant.Equal(current.Grants[b.Sender])
	}
	if !unchanged || ctx.Err() != nil {
		return deny()
	}
	digest := localCheckpointKey("wechat-cross-request-v1", preview.Digest, grants.Revision, source, sourceGrant, originGrant, privateGrant)
	if forward {
		digest = localCheckpointKey("wechat-forward-v1", digest, sourceFile, targetFile)
	}
	if stationSource {
		digest = localCheckpointKey("wechat-to-station-v1", digest, owner)
	}
	return stationWechatCrossAccess{Digest: digest, PrivateTarget: b.Sender, GrantRevision: grants.Revision}, nil
}
