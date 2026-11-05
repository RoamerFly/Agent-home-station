package admin

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/linuxnative"
)

var errStationMutePending = errors.New("previous mute outcome pending")
var errStationMuteTarget = errors.New("group target is not verified")
var errStationMuteUnsupported = errors.New("group notification mutation is not advertised")
var errStationMuteGrant = &stationContactError{"wechat_group_mute_not_authorized", "本地聊天没有该连接和群的免打扰授权，或本轮授权已变更。"}

type stationMuteInput struct {
	Group string `json:"group_id"`
	Muted *bool  `json:"muted"`
	Key   string `json:"request_key"`
}

type stationMuteAuthority struct {
	requester string
	trace     string
	grant     time.Time
	active    func(context.Context) bool
}

func validStationGroup(id string) bool {
	return validID(id) && strings.HasSuffix(id, "@chatroom") && id != "@chatroom"
}

func (s *stationWechat) groupActionRoutes(owner *Server, mux *http.ServeMux, identity *fishIdentityRegistry) {
	const base = "/api/apps/wechat/connections/{connection}"
	mux.HandleFunc("GET "+base+"/groups/{group}/notification", owner.protected(s.stationGroupNotification))
	mux.HandleFunc("POST "+base+"/group-actions", owner.protected(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		var in stationMuteInput
		if !decode(w, r, &in) {
			return
		}
		if !validStationGroup(in.Group) || in.Muted == nil || !validID(in.Key) || !artifactID(r.PathValue("connection"), "wechat-") {
			fail(w, http.StatusBadRequest, "请提供连接、群、免打扰状态和请求编号")
			return
		}
		record, err := identity.localIdentity()
		if err != nil || record.LocalOwner == nil {
			fail(w, http.StatusConflict, "请先准备本地主人身份")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		stop := context.AfterFunc(s.ctx, cancel)
		defer stop()
		authority := stationMuteAuthority{requester: record.LocalOwner.ID, active: func(context.Context) bool {
			_, _, ok := owner.session(r)
			current, err := identity.localIdentity()
			return ok && err == nil && current.FishID == record.FishID && current.LocalOwner != nil && current.LocalOwner.ID == record.LocalOwner.ID
		}}
		out, err := s.submitStationMute(ctx, r.PathValue("connection"), in, authority)
		switch {
		case err == nil:
			respond(w, http.StatusAccepted, out)
		case errors.Is(err, errStationMutePending):
			fail(w, http.StatusConflict, "此群已有结果待核对的操作，请先查询原操作")
		case errors.Is(err, errStationMuteUnsupported):
			fail(w, http.StatusConflict, "当前微信后台未声明群免打扰能力")
		case errors.Is(err, errStationMuteTarget), errors.Is(err, linuxnative.ErrToolNotFound), errors.Is(err, apphost.ErrNotFound):
			fail(w, http.StatusNotFound, "未能在该连接核对所选群")
		case errors.Is(err, apphost.ErrLifecycleConflict), errors.Is(err, errContactLogin):
			fail(w, http.StatusConflict, "连接已暂停、未登录或请求内容变化，未提交操作")
		default:
			fail(w, http.StatusServiceUnavailable, "暂时无法核对操作，请查看该群的原操作记录")
		}
	}))
}

func (w *stationWechatWorker) actionLedgerLocked() (*actionLedger, error) {
	if w.actions == nil {
		ledger, err := newActionLedger(w.dir)
		if err != nil {
			return nil, err
		}
		w.actions = ledger
	}
	return w.actions, nil
}

func (s *stationWechat) findStationMute(l *actionLedger, connection, id, group string) (actionProposal, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var latest actionProposal
	for _, p := range l.items {
		if p.AppAction == nil || p.AppAction.ConnectionRef != s.connectionRef(connection, p.Account) ||
			wechatapp.ConnectionID(p.Account) != connection || p.Request.Kind != "mute" ||
			p.AppAction.Action != "mute" || p.AppAction.Target != p.Request.ThreadID || p.AppAction.IdempotencyKey != p.ID {
			continue
		}
		if id != "" && p.ID == id {
			return p, true
		}
		if id == "" && p.Request.ThreadID == group && (latest.ID == "" || p.Created.After(latest.Created)) {
			latest = p
		}
	}
	return latest, latest.ID != ""
}

func (s *stationWechat) submitStationMute(ctx context.Context, connection string, in stationMuteInput, authority stationMuteAuthority) (apphost.ActionReceipt, error) {
	var out apphost.ActionReceipt
	account, err := s.signedInAccount(ctx, connection)
	if err != nil {
		return out, err
	}
	native, err := s.WorkerSender(ctx, connection)
	if err != nil {
		return out, err
	}
	if native.Account() != account {
		return out, apphost.ErrUnavailable
	}
	caps, err := native.Capabilities(ctx)
	if err != nil {
		return out, err
	}
	if !slices.Contains(caps.ThreadNotificationTargets, "group") {
		return out, errStationMuteUnsupported
	}
	group, err := native.GroupNotification(ctx, in.Group)
	if err != nil {
		return out, err
	}
	if group.InGroup != nil && !*group.InGroup {
		return out, errStationMuteTarget
	}
	if _, err := s.signedInAccount(ctx, connection); err != nil {
		return out, err
	}
	w, err := s.worker(connection, true)
	if err != nil {
		return out, err
	}
	key := localCheckpointKey(connection, authority.requester, in.Key)
	if authority.trace != "" {
		key = localCheckpointKey(connection, authority.requester, "local_chat", authority.trace, in.Group)
	}
	p := actionProposal{ID: "station-mute-" + key, Account: account, State: "unknown",
		Request: linuxnative.AdminAction{Kind: "mute", ThreadID: in.Group, Muted: *in.Muted}}
	intent := apphost.ActionIntent{ConnectionRef: s.connectionRef(connection, account), Action: "mute",
		RequesterID: authority.requester, Source: "local_admin", SourceRequestID: p.ID, Target: in.Group,
		Purpose: "owner_approved_group_notification", GrantVersion: "owner_approval:" + p.ID, IdempotencyKey: p.ID}
	if authority.trace != "" {
		intent.Source, intent.SourceRequestID = "local_chat", authority.trace
		intent.Purpose = "agent_group_notification"
		intent.GrantVersion = "local_chat_mute:" + authority.grant.Format(time.RFC3339Nano)
	}
	p.AppAction = &intent
	w.mu.Lock()
	if ctx.Err() != nil || !authority.active(ctx) {
		w.mu.Unlock()
		return out, apphost.ErrLifecycleConflict
	}
	if authority.trace != "" && (authority.grant.IsZero() || !w.policy.GroupMuteGrants[in.Group].Equal(authority.grant)) {
		w.mu.Unlock()
		return out, errStationMuteGrant
	}
	ledger, err := w.actionLedgerLocked()
	if err != nil {
		w.mu.Unlock()
		return out, err
	}
	if previous, ok := s.findStationMute(ledger, connection, p.ID, ""); ok {
		w.mu.Unlock()
		if previous.Request.ThreadID != in.Group || previous.Request.Muted != *in.Muted {
			return out, apphost.ErrLifecycleConflict
		}
		return stationMuteReceipt(previous), nil
	}
	if w.policy.Paused {
		w.mu.Unlock()
		return out, apphost.ErrLifecycleConflict
	}
	previous, ok := s.findStationMute(ledger, connection, "", in.Group)
	if ok && !stationMuteFinal(previous.State) {
		w.mu.Unlock()
		return out, errStationMutePending
	}
	p.Created = time.Now().UTC()
	if ok && !p.Created.After(previous.Created) {
		p.Created = previous.Created.Add(time.Nanosecond)
	}
	err = ledger.save(p)
	w.mu.Unlock()
	if err != nil {
		return out, err
	}
	// Reservation is durable and unique before handing the action to native.
	// A lost response leaves the original reservation for read-only inspection.
	status, err := native.ExecuteAction(ctx, p.Request, intent.IdempotencyKey)
	if err != nil || !wechatapp.MatchGroupMuteReceipt(status, p.Account, p.Request.ThreadID, "") {
		return stationMuteReceipt(p), nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, prior := range ledger.list(p.Account) {
		if prior.ID != p.ID && prior.Result.ID == status.ID {
			return stationMuteReceipt(p), nil
		}
	}
	p.Result, p.State = status, status.State
	if err := ledger.save(p); err != nil {
		return out, err
	}
	return stationMuteReceipt(p), nil
}

func stationMuteFinal(state string) bool {
	return state == "succeeded" || state == "failed" || state == "rejected" || state == "cancelled"
}

func stationMuteReceipt(p actionProposal) apphost.ActionReceipt {
	out := apphost.ActionReceipt{ConnectionRef: p.AppAction.ConnectionRef, OperationID: p.ID, Action: "mute",
		Target: p.Request.ThreadID, State: "submission_unknown", Lookup: "stored"}
	if p.Result.ID != "" {
		projectAccountAction(&out, p.Result)
	}
	return out
}

func (s *stationWechat) lookupStationMute(ctx context.Context, ledger *actionLedger, p actionProposal) apphost.ActionReceipt {
	out := stationMuteReceipt(p)
	if p.Result.ID == "" || stationMuteFinal(p.State) {
		return out
	}
	native, err := s.WorkerSender(ctx, p.AppAction.ConnectionID)
	if err != nil || native.Account() != p.Account {
		out.Lookup = "unavailable"
		return out
	}
	status, err := native.InspectAction(ctx, p.Result.ID)
	if err != nil {
		out.Lookup = "unavailable"
		return out
	}
	if !wechatapp.MatchGroupMuteReceipt(status, p.Account, p.Request.ThreadID, p.Result.ID) {
		out.Lookup = "identity_mismatch"
		return out
	}
	// Persist only terminal progress here. Concurrent GETs cannot replace a
	// confirmed receipt with an older queued response.
	if stationMuteFinal(status.State) {
		p.Result, p.State = status, status.State
		if err := ledger.save(p); err != nil {
			out.Lookup = "unavailable"
			return out
		}
	}
	out.Lookup = "platform"
	projectAccountAction(&out, status)
	return out
}

func (s *stationWechat) stationMuteLedger(connection string) (*actionLedger, bool, error) {
	if !artifactID(connection, "wechat-") {
		return nil, false, apphost.ErrNotFound
	}
	w, err := s.worker(connection, false)
	if err != nil || w == nil {
		return nil, false, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	ledger, err := w.actionLedgerLocked()
	return ledger, w.policy.Paused, err
}

func (s *stationWechat) ReadAction(ctx context.Context, connection, id string) (apphost.ActionReceipt, error) {
	ledger, _, err := s.stationMuteLedger(connection)
	if err != nil {
		return apphost.ActionReceipt{}, err
	}
	if ledger == nil || !validID(id) {
		return apphost.ActionReceipt{}, apphost.ErrNotFound
	}
	p, ok := s.findStationMute(ledger, connection, id, "")
	if !ok {
		return apphost.ActionReceipt{}, apphost.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.lookupStationMute(ctx, ledger, p), nil
}

func (s *stationWechat) stationGroupNotification(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, group := r.PathValue("connection"), r.PathValue("group")
	if !validStationGroup(group) || !artifactID(id, "wechat-") {
		fail(w, http.StatusBadRequest, "连接或群编号无效")
		return
	}
	ledger, paused, err := s.stationMuteLedger(id)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "暂时无法核对该群操作记录")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result := map[string]any{}
	pending := false
	if ledger != nil {
		if p, ok := s.findStationMute(ledger, id, "", group); ok {
			receipt := s.lookupStationMute(ctx, ledger, p)
			result["operation"] = receipt
			pending = !stationMuteFinal(receipt.State)
		}
	}
	account, err := s.signedInAccount(ctx, id)
	if err == nil {
		var native *linuxnative.Sender
		native, err = s.WorkerSender(ctx, id)
		if err == nil && native.Account() == account {
			var state linuxnative.GroupNotification
			state, err = native.GroupNotification(ctx, group)
			if err == nil {
				if state.Muted != nil {
					result["muted"] = *state.Muted
				}
				canChange := !paused && !pending && (state.InGroup == nil || *state.InGroup)
				if canChange {
					caps, err := native.Capabilities(ctx)
					canChange = err == nil && slices.Contains(caps.ThreadNotificationTargets, "group")
				}
				result["can_change"] = canChange
				result["coverage"] = "cached"
				respond(w, http.StatusOK, result)
				return
			}
		}
	}
	if result["operation"] != nil {
		result["can_change"], result["coverage"] = false, "unavailable"
		respond(w, http.StatusOK, result)
		return
	}
	if errors.Is(err, apphost.ErrNotFound) || errors.Is(err, linuxnative.ErrToolNotFound) {
		fail(w, http.StatusNotFound, "未能在该连接核对所选群")
		return
	}
	fail(w, http.StatusServiceUnavailable, "暂时无法核对所选群的免打扰状态")
}
