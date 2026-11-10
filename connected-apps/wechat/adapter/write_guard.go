package adapter

import (
	"context"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/linuxnative"
)

// GuardWorkerWrites binds a scoped worker sender to the host's current
// dispatch admission. Read-only queries and cancellation of prior jobs retain
// the native protocol's separate handling.
func GuardWorkerWrites(native *linuxnative.Sender, connectionID string, admit func(context.Context) (func(), error)) error {
	if native == nil || native.Account() == "" || ConnectionID(native.Account()) != connectionID || admit == nil {
		return apphost.ErrLifecycleCutover
	}
	native.GuardChanges(admit)
	return nil
}

// CheckManagedWriteTarget verifies the live WeChat account and dataset.
// The host supplies the additional evidence required by the handoff.
func CheckManagedWriteTarget(ctx context.Context, native *linuxnative.Sender, dataset string, requireOnline bool, checkDaemon func(linuxnative.DaemonStatus) bool) error {
	if native == nil || native.Account() == "" || dataset == "" || checkDaemon == nil {
		return apphost.ErrLifecycleCutover
	}
	daemon, err := native.DaemonStatus(ctx)
	if err != nil || daemon.ServerID != dataset || !checkDaemon(daemon) {
		return apphost.ErrLifecycleCutover
	}
	status, err := native.Status(ctx)
	if err != nil || status.AccountID != native.Account() || status.StorageMode != "managed" ||
		requireOnline && TargetLoginState(status, native.Account()) != "online" || ctx.Err() != nil {
		return apphost.ErrLifecycleCutover
	}
	return nil
}

// TargetLoginState describes the observed login of the exact account.
func TargetLoginState(status linuxnative.Status, account string) string {
	login := "pending"
	if status.AccountID == account && status.LoginObserved {
		switch {
		case !status.Authenticated:
			login = "signed_out"
		case status.Connection == "online" || status.Connection == "connected":
			login = "online"
		default:
			login = "authenticated_offline"
		}
	}
	return login
}
