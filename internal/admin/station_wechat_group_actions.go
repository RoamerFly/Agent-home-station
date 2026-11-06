package admin

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/stationservices"

	"wechat-bot/internal/websession"
)

var errStationGroupActionPending = errors.New("previous group action outcome pending")
var errStationGroupActionTarget = errors.New("group target is not verified")
var errStationGroupActionUnsupported = errors.New("group action is not advertised")
var errStationMuteGrant = &stationContactError{"wechat_group_mute_not_authorized", "本地聊天没有该连接和群的免打扰授权，或本轮授权已变更。"}

type stationGroupActionInput struct {
	Action string  `json:"action"`
	Name   *string `json:"name"`
	Group  string  `json:"group_id"`
	Muted  *bool   `json:"muted"`
	Key    string  `json:"request_key"`
}

type stationGroupActionAuthority struct {
	requester string
	trace     string
	grant     time.Time
	active    func(context.Context) bool
}

func validStationGroup(id string) bool {
	return validID(id) && strings.HasSuffix(id, "@chatroom") && id != "@chatroom"
}

func (s *stationWechat) groupActionRoutes(owner *websession.Store, mux *http.ServeMux, identity stationservices.OwnerReader) {
	const base = "/api/apps/wechat/connections/{connection}"
	mux.HandleFunc("GET "+base+"/groups/{group}/notification", owner.Protect(s.stationGroupDetails("mute")))
	mux.HandleFunc("GET "+base+"/groups/{group}/profile", owner.Protect(s.stationGroupDetails("rename", "my_name")))
	mux.HandleFunc("POST "+base+"/group-actions", owner.Protect(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		var in stationGroupActionInput
		if !decode(w, r, &in) {
			return
		}
		action := linuxnative.AdminAction{Kind: in.Action, ThreadID: in.Group}
		if action.Kind == "" {
			action.Kind = "mute"
		}
		if !validStationGroup(in.Group) || !validID(in.Key) || !artifactID(r.PathValue("connection"), "wechat-") {
			fail(w, http.StatusBadRequest, "请提供有效的连接、群 ID 和请求编号")
			return
		}
		switch {
		case action.Kind == "mute" && in.Muted != nil && (in.Name == nil || *in.Name == ""):
			action.Muted = *in.Muted
		case (action.Kind == "rename" || action.Kind == "my_name") && in.Muted == nil &&
			in.Name != nil && validStationGroupName(*in.Name, action.Kind == "my_name"):
			action.Text = *in.Name
		default:
			fail(w, http.StatusBadRequest, "请提供有效的群操作。群名称须非空，本人群昵称可明确提交空字符串。名称最多 512 UTF-8 字节，且不能含控制字符")
			return
		}
		record, err := identity()
		if err != nil {
			fail(w, http.StatusConflict, "请先准备本地主人身份")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		stop := context.AfterFunc(s.ctx, cancel)
		defer stop()
		authority := stationGroupActionAuthority{requester: record.ID, active: func(context.Context) bool {
			_, _, ok := owner.Lookup(r)
			current, err := identity()
			return ok && err == nil && current == record
		}}
		out, err := s.submitStationGroupAction(ctx, r.PathValue("connection"), action, in.Key, authority)
		switch {
		case err == nil:
			respond(w, http.StatusAccepted, out)
		case errors.Is(err, errStationGroupActionPending):
			fail(w, http.StatusConflict, "此群已有结果待核对的操作，请先查询原操作")
		case errors.Is(err, errStationGroupActionUnsupported):
			fail(w, http.StatusConflict, "当前微信后台未声明此群操作")
		case errors.Is(err, errStationGroupActionTarget), errors.Is(err, linuxnative.ErrToolNotFound), errors.Is(err, apphost.ErrNotFound):
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

func (s *stationWechat) findStationAction(l *actionLedger, connection, id, group, kind string) (actionProposal, bool) {
	if l == nil {
		return actionProposal{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var latest actionProposal
	for _, p := range l.items {
		if p.AppAction == nil || p.AppAction.ConnectionRef != s.connectionRef(connection, p.Account) ||
			wechatapp.ConnectionID(p.Account) != connection ||
			!(stationGroupActionKind(p.Request.Kind) || id != "" && p.Request.Kind == "recall") || kind != "" && p.Request.Kind != kind ||
			p.AppAction.Action != p.Request.Kind || p.AppAction.Target != p.Request.ThreadID || p.AppAction.IdempotencyKey != p.ID {
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

func (s *stationWechat) submitStationGroupAction(ctx context.Context, connection string, action linuxnative.AdminAction, requestKey string, authority stationGroupActionAuthority) (apphost.ActionReceipt, error) {
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
	if !stationGroupActionSupported(caps, action.Kind) {
		return out, errStationGroupActionUnsupported
	}
	group, err := native.GroupProfile(ctx, action.ThreadID)
	if err != nil {
		return out, err
	}
	if group.InGroup != nil && !*group.InGroup {
		return out, errStationGroupActionTarget
	}
	current, err := s.signedInAccount(ctx, connection)
	if err != nil {
		return out, err
	}
	if current != account {
		return out, errStationGroupActionTarget
	}
	w, err := s.worker(connection, true)
	if err != nil {
		return out, err
	}
	key := localCheckpointKey(connection, authority.requester, requestKey)
	if authority.trace != "" {
		key = localCheckpointKey(connection, authority.requester, "local_chat", authority.trace, action.ThreadID)
	}
	p := actionProposal{ID: "station-" + action.Kind + "-" + key, Account: account, State: "unknown", Request: action}
	purpose := "owner_approved_group_notification"
	switch action.Kind {
	case "rename":
		purpose = "owner_approved_group_rename"
	case "my_name":
		purpose = "owner_approved_group_my_name"
	}
	intent := apphost.ActionIntent{ConnectionRef: s.connectionRef(connection, account), Action: action.Kind,
		RequesterID: authority.requester, Source: "local_admin", SourceRequestID: p.ID, Target: action.ThreadID,
		Purpose: purpose, GrantVersion: "owner_approval:" + p.ID, IdempotencyKey: p.ID}
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
	if authority.trace != "" && (action.Kind != "mute" || authority.grant.IsZero() || !w.policy.GroupMuteGrants[action.ThreadID].Equal(authority.grant)) {
		w.mu.Unlock()
		return out, errStationMuteGrant
	}
	ledger, err := w.actionLedgerLocked()
	if err != nil {
		w.mu.Unlock()
		return out, err
	}
	if previous, ok := s.findStationAction(ledger, connection, p.ID, "", action.Kind); ok {
		w.mu.Unlock()
		if previous.Request.ThreadID != action.ThreadID || previous.Request.Muted != action.Muted || previous.Request.Text != action.Text {
			return out, apphost.ErrLifecycleConflict
		}
		return stationActionReceipt(previous), nil
	}
	if w.policy.Paused {
		w.mu.Unlock()
		return out, apphost.ErrLifecycleConflict
	}
	previous, ok := s.findStationAction(ledger, connection, "", action.ThreadID, action.Kind)
	if ok && !stationActionFinal(previous.State) {
		w.mu.Unlock()
		return out, errStationGroupActionPending
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
	if err != nil || !matchesStationActionReceipt(p, status) {
		return stationActionReceipt(p), nil
	}
	return ledger.recordStationActionReceipt(p, status)
}

func validStationGroupName(name string, allowEmpty bool) bool {
	return utf8.ValidString(name) && len(name) <= 512 && (allowEmpty || strings.TrimSpace(name) != "") && strings.IndexFunc(name, unicode.IsControl) < 0
}

func stationGroupActionKind(kind string) bool {
	return kind == "mute" || kind == "rename" || kind == "my_name"
}

func stationGroupActionSupported(caps linuxnative.Capabilities, kind string) bool {
	if kind == "mute" {
		return slices.Contains(caps.ThreadNotificationTargets, "group")
	}
	return stationGroupActionKind(kind) && slices.Contains(caps.GroupMutations, kind)
}

func stationActionFinal(state string) bool {
	return state == "succeeded" || state == "failed" || state == "rejected" || state == "cancelled"
}

func stationActionReceipt(p actionProposal) apphost.ActionReceipt {
	out := apphost.ActionReceipt{ConnectionRef: p.AppAction.ConnectionRef, OperationID: p.ID, Action: p.Request.Kind,
		Target: p.Request.ThreadID, State: "submission_unknown", Lookup: "stored"}
	if p.Result.ID != "" {
		wechatapp.ProjectActionReceipt(&out, p.Result)
	}
	return out
}

func (s *stationWechat) lookupStationAction(ctx context.Context, ledger *actionLedger, p actionProposal) apphost.ActionReceipt {
	out := stationActionReceipt(p)
	if stationActionFinal(p.State) || p.Request.Kind == "recall" && p.Result.ID == "" {
		return out
	}
	native, err := s.WorkerSender(ctx, p.AppAction.ConnectionID)
	if err != nil || native.Account() != p.Account {
		out.Lookup = "unavailable"
		return out
	}
	var status linuxnative.ActionStatus
	if p.Result.ID == "" {
		status, err = native.InspectGroupActionKey(ctx, p.AppAction.IdempotencyKey)
		if errors.Is(err, linuxnative.ErrToolNotFound) {
			return out
		}
	} else {
		status, err = native.InspectAction(ctx, p.Result.ID)
	}
	if err != nil {
		out.Lookup = "unavailable"
		return out
	}
	if !matchesStationActionReceipt(p, status) {
		out.Lookup = "identity_mismatch"
		return out
	}
	out, err = ledger.recordStationActionReceipt(p, status)
	if err != nil {
		out.Lookup = "unavailable"
	}
	return out
}

func matchesStationActionReceipt(p actionProposal, status linuxnative.ActionStatus) bool {
	if p.Request.Kind == "recall" {
		return wechatapp.MatchRecallReceipt(status, p.Account, p.Request, p.Result.ID)
	}
	return wechatapp.MatchGroupActionReceipt(status, p.Account, p.Request.Kind, p.Request.ThreadID, p.Result.ID)
}

func (l *actionLedger) recordStationActionReceipt(p actionProposal, status linuxnative.ActionStatus) (apphost.ActionReceipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	current := l.items[p.ID]
	out := stationActionReceipt(current)
	if stationActionFinal(current.State) {
		return out, nil
	}
	if current.Result.ID != "" && current.Result.ID != status.ID {
		out.Lookup = "identity_mismatch"
		return out, nil
	}
	for id, prior := range l.items {
		if id != p.ID && prior.Account == p.Account && prior.Result.ID == status.ID {
			out.Lookup = "identity_mismatch"
			return out, nil
		}
	}
	// Keep the first verified native operation ID and terminal results durable.
	// Concurrent reads cannot replace a terminal receipt with older progress.
	if current.Result.ID == "" || stationActionFinal(status.State) {
		current.Result, current.State = status, status.State
		if err := l.saveLocked(current); err != nil {
			return out, err
		}
	}
	out.Lookup = "platform"
	wechatapp.ProjectActionReceipt(&out, status)
	return out, nil
}

func (s *stationWechat) stationActionLedger(connection string) (*actionLedger, bool, error) {
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
	ledger, _, err := s.stationActionLedger(connection)
	if err != nil {
		return apphost.ActionReceipt{}, err
	}
	if ledger == nil || !validID(id) {
		return apphost.ActionReceipt{}, apphost.ErrNotFound
	}
	p, ok := s.findStationAction(ledger, connection, id, "", "")
	if !ok {
		return apphost.ActionReceipt{}, apphost.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.lookupStationAction(ctx, ledger, p), nil
}

type stationGroupDetailFields struct {
	action, available, operation string
}

func (s *stationWechat) stationGroupDetails(kinds ...string) http.HandlerFunc {
	fields := make([]stationGroupDetailFields, 0, len(kinds))
	for _, kind := range kinds {
		f := stationGroupDetailFields{action: kind, available: "can_change", operation: "operation"}
		switch kind {
		case "rename":
			f.available = "can_rename"
		case "my_name":
			f.available, f.operation = "can_set_my_name", "my_name_operation"
		}
		fields = append(fields, f)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		s.readStationGroupDetails(w, r, fields)
	}
}

func (s *stationWechat) readStationGroupDetails(w http.ResponseWriter, r *http.Request, fields []stationGroupDetailFields) {
	w.Header().Set("Cache-Control", "no-store")
	id, group := r.PathValue("connection"), r.PathValue("group")
	if !validStationGroup(group) || !artifactID(id, "wechat-") {
		fail(w, http.StatusBadRequest, "连接或群编号无效")
		return
	}
	ledger, paused, err := s.stationActionLedger(id)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "暂时无法核对该群操作记录")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result := map[string]any{}
	hasReceipt := false
	for _, field := range fields {
		result[field.available] = false
		if ledger == nil {
			continue
		}
		if p, ok := s.findStationAction(ledger, id, "", group, field.action); ok {
			lookupCtx, cancelLookup := context.WithTimeout(ctx, 3*time.Second)
			result[field.operation] = s.lookupStationAction(lookupCtx, ledger, p)
			cancelLookup()
			hasReceipt = true
		}
	}

	account, err := s.signedInAccount(ctx, id)
	if err == nil {
		var native *linuxnative.Sender
		native, err = s.WorkerSender(ctx, id)
		if err == nil && native.Account() == account {
			var state linuxnative.GroupProfile
			state, err = native.GroupProfile(ctx, group)
			if err == nil {
				var caps linuxnative.Capabilities
				canChange := !paused && (state.InGroup == nil || *state.InGroup)
				if canChange {
					caps, err = native.Capabilities(ctx)
					canChange = err == nil
				}
				for _, field := range fields {
					receipt, _ := result[field.operation].(apphost.ActionReceipt)
					pending := receipt.OperationID != "" && !stationActionFinal(receipt.State)
					result[field.available] = canChange && !pending && stationGroupActionSupported(caps, field.action)
					switch field.action {
					case "mute":
						if state.Muted != nil {
							result["muted"] = *state.Muted
						}
					case "rename":
						if state.Name != "" {
							result["name"] = state.Name
						}
					case "my_name":
						if state.MyDisplayName != nil {
							result["my_display_name"] = *state.MyDisplayName
						}
					}
				}
				result["coverage"] = "cached"
				respond(w, http.StatusOK, result)
				return
			}
		}
	}
	if hasReceipt {
		result["coverage"] = "unavailable"
		respond(w, http.StatusOK, result)
		return
	}
	if errors.Is(err, apphost.ErrNotFound) || errors.Is(err, linuxnative.ErrToolNotFound) {
		fail(w, http.StatusNotFound, "未能在该连接核对所选群")
		return
	}
	fail(w, http.StatusServiceUnavailable, "暂时无法核对所选群资料")
}
