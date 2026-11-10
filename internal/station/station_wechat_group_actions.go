package station

import (
	"context"
	"time"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/linuxnative"
)

var errStationMuteGrant = &stationContactError{"wechat_group_mute_not_authorized", "本地聊天没有该连接和群的免打扰授权，或本轮授权已变更。"}

type stationGroupActionAuthority struct {
	requester string
	trace     string
	grant     time.Time
	active    func(context.Context) bool
}

func (w *stationWechatWorker) actionLedgerLocked() (*wechatapp.ActionLedger, error) {
	if w.actions == nil {
		ledger, err := wechatapp.OpenActionLedger(w.runtimeStateDir())
		if err != nil {
			return nil, err
		}
		w.actions = ledger
	}
	return w.actions, nil
}

func (s *stationWechat) submitStationGroupAction(ctx context.Context, connection string, action linuxnative.AdminAction, requestKey string, authority stationGroupActionAuthority) (apphost.ActionReceipt, error) {
	var out apphost.ActionReceipt
	native, err := s.directoryAccess().PrepareGroupAction(ctx, connection, action)
	if err != nil {
		return out, err
	}
	account := native.Account()
	w, err := s.worker(connection, true)
	if err != nil {
		return out, err
	}
	key := localCheckpointKey(connection, authority.requester, requestKey)
	if authority.trace != "" {
		key = localCheckpointKey(connection, authority.requester, "local_chat", authority.trace, action.ThreadID)
	}
	p := wechatapp.ActionProposal{ID: "station-" + action.Kind + "-" + key, Account: account, State: "unknown", Request: action}
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
	if previous, ok := ledger.FindConnected(s.installation, connection, p.ID, "", action.Kind); ok {
		w.mu.Unlock()
		if previous.Request.ThreadID != action.ThreadID || previous.Request.Muted != action.Muted || previous.Request.Text != action.Text {
			return out, apphost.ErrLifecycleConflict
		}
		return previous.Receipt(), nil
	}
	if w.policy.Paused {
		w.mu.Unlock()
		return out, apphost.ErrLifecycleConflict
	}
	if err := s.admitPlatformWrite(ctx, connection, native); err != nil {
		w.mu.Unlock()
		return out, err
	}
	previous, ok := ledger.FindConnected(s.installation, connection, "", action.ThreadID, action.Kind)
	if ok && !wechatapp.IsFinalActionState(previous.State) {
		w.mu.Unlock()
		return out, wechatapp.ErrGroupActionPending
	}
	p.Created = time.Now().UTC()
	if ok && !p.Created.After(previous.Created) {
		p.Created = previous.Created.Add(time.Nanosecond)
	}
	submit, err := ledger.ReserveConnected(p, native)
	w.mu.Unlock()
	if err != nil {
		return out, err
	}
	// Reservation is durable and unique before handing the action to native.
	// A lost response leaves the original reservation for read-only inspection.
	return submit(ctx)
}

func (s *stationWechat) lookupStationAction(ctx context.Context, ledger *wechatapp.ActionLedger, p wechatapp.ActionProposal) apphost.ActionReceipt {
	return ledger.RefreshConnected(ctx, p, s.WorkerSender)
}

func (s *stationWechat) stationActionLedger(connection string) (*wechatapp.ActionLedger, bool, error) {
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
	if ledger == nil || !identity.ValidPathID(id) {
		return apphost.ActionReceipt{}, apphost.ErrNotFound
	}
	p, ok := ledger.FindConnected(s.installation, connection, id, "", "")
	if !ok {
		return apphost.ActionReceipt{}, apphost.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.lookupStationAction(ctx, ledger, p), nil
}

func (s *stationWechat) stationGroupDetails(ctx context.Context, id, group string, kinds []string) (wechatapp.GroupManagementDetails, error) {
	out := wechatapp.GroupManagementDetails{Receipts: make(map[string]apphost.ActionReceipt)}
	ledger, paused, err := s.stationActionLedger(id)
	if err != nil {
		return out, wechatapp.ErrGroupDetailsLedger
	}
	if ledger != nil {
		for _, kind := range kinds {
			if p, ok := ledger.FindConnected(s.installation, id, "", group, kind); ok {
				lookup, cancel := context.WithTimeout(ctx, 3*time.Second)
				out.Receipts[kind] = s.lookupStationAction(lookup, ledger, p)
				cancel()
			}
		}
	}
	details, err := s.directoryAccess().ReadGroupDetails(ctx, id, group, paused)
	details.Receipts = out.Receipts
	return details, err
}
