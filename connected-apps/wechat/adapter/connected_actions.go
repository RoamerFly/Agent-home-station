package adapter

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/linuxnative"
)

// ReserveConnected records a new, already authorized action before providing a
// single-use submit function. Reopened reservations only support inspection.
func (l *ActionLedger) ReserveConnected(p ActionProposal, native *linuxnative.Sender) (func(context.Context) (apphost.ActionReceipt, error), error) {
	if p.ID == "" || p.State != "unknown" || p.Result.ID != "" || p.AppAction == nil || !connectedActionMatches(p, p.AppAction.ConnectionRef) ||
		(!IsManagedGroupAction(p.Request.Kind) && p.Request.Kind != "recall") || native == nil || native.Account() != p.Account {
		return nil, apphost.ErrLifecycleConflict
	}
	p = cloneActionProposal(p)
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.items[p.ID]; exists {
		return nil, apphost.ErrLifecycleConflict
	}
	if err := l.saveLocked(p); err != nil {
		return nil, err
	}
	var used atomic.Bool
	return func(ctx context.Context) (apphost.ActionReceipt, error) {
		if used.Swap(true) {
			return p.Receipt(), apphost.ErrLifecycleConflict
		}
		return submitReservedAction(ctx, native, l, p)
	}, nil
}

func submitReservedAction(ctx context.Context, native *linuxnative.Sender, ledger *ActionLedger, p ActionProposal) (apphost.ActionReceipt, error) {
	status, err := native.ExecuteAction(ctx, p.Request, p.AppAction.IdempotencyKey)
	if errors.Is(err, linuxnative.ErrWriteNotAdmitted) {
		if err := ledger.RejectUnsubmitted(p); err != nil {
			return p.Receipt(), nil
		}
		return apphost.ActionReceipt{}, apphost.ErrLifecycleCutover
	}
	if err != nil || !p.MatchesReceipt(status) {
		return p.Receipt(), nil
	}
	return ledger.RecordConnectedReceipt(p, status)
}

// RefreshConnected inspects the original operation and persists verified native
// evidence. It never retries a write, even when the native key is not found.
func (l *ActionLedger) RefreshConnected(ctx context.Context, p ActionProposal, sender func(context.Context, string) (*linuxnative.Sender, error)) apphost.ActionReceipt {
	out := p.Receipt()
	if p.AppAction == nil || !connectedActionMatches(p, p.AppAction.ConnectionRef) || IsFinalActionState(p.State) || !canInspectAction(p) {
		return out
	}
	native, err := sender(ctx, p.AppAction.ConnectionID)
	if err != nil {
		out.Lookup = "unavailable"
		return out
	}
	status, lookup := inspectAction(ctx, native, p)
	if lookup == "not_found" && p.Result.ID == "" {
		return out
	}
	if lookup != "platform" {
		if lookup == "not_found" {
			lookup = "unavailable"
		}
		out.Lookup = lookup
		return out
	}
	out, err = l.RecordConnectedReceipt(p, status)
	if err != nil {
		out.Lookup = "unavailable"
	}
	return out
}

// ProjectRollbackActions checks native receipts for the sealed audit without
// changing the durable snapshot.
func ProjectRollbackActions(ctx context.Context, native *linuxnative.Sender, proposals []ActionProposal) []apphost.ActionReceipt {
	out := make([]apphost.ActionReceipt, 0, len(proposals))
	for _, p := range proposals {
		receipt := p.Receipt()
		if native != nil && !IsFinalActionState(receipt.State) && canInspectAction(p) {
			lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			status, lookup := inspectAction(lookupCtx, native, p)
			cancel()
			receipt.Lookup = lookup
			if lookup == "platform" {
				ProjectActionReceipt(&receipt, status)
			}
		}
		out = append(out, receipt)
	}
	return out
}

// The native API has no recall lookup by request key. Keep unknown recalls
// without an operation ID available only as local evidence.
func canInspectAction(p ActionProposal) bool {
	return p.Result.ID != "" || p.Request.Kind != "recall"
}

func inspectAction(ctx context.Context, native *linuxnative.Sender, p ActionProposal) (linuxnative.ActionStatus, string) {
	if native == nil || native.Account() != p.Account || ctx.Err() != nil {
		return linuxnative.ActionStatus{}, "unavailable"
	}
	var status linuxnative.ActionStatus
	var err error
	if p.Result.ID == "" {
		status, err = native.InspectGroupActionKey(ctx, p.ID)
	} else {
		status, err = native.InspectAction(ctx, p.Result.ID)
	}
	switch {
	case errors.Is(err, linuxnative.ErrToolNotFound):
		return status, "not_found"
	case err != nil:
		return status, "unavailable"
	case !p.MatchesReceipt(status):
		return status, "identity_mismatch"
	default:
		return status, "platform"
	}
}
