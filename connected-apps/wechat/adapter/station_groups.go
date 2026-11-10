package adapter

import (
	"context"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/linuxnative"
)

// ReadGroupDetails reads the cached profile and optional action capabilities.
// The host supplies the pause state and adds any receipts from its own ledger.
func (a StationDirectoryAccess) ReadGroupDetails(ctx context.Context, connection, group string, paused bool) (GroupManagementDetails, error) {
	var out GroupManagementDetails
	native, err := a.accountSender(ctx, connection, "")
	if err != nil {
		return out, err
	}
	profile, err := native.GroupProfile(ctx, group)
	if err != nil {
		return out, err
	}
	if !paused && (profile.InGroup == nil || *profile.InGroup) {
		out.Capabilities, err = native.Capabilities(ctx)
		out.CanChange = err == nil
	}
	current, err := a.SignedInAccount(ctx, connection)
	if err != nil {
		return GroupManagementDetails{}, err
	}
	if current != native.Account() {
		return GroupManagementDetails{}, apphost.ErrLifecycleConflict
	}
	out.Profile = &profile
	return out, nil
}

// PrepareGroupAction verifies the native operation and target. The host still
// checks authorization and reserves the original operation before submission.
func (a StationDirectoryAccess) PrepareGroupAction(ctx context.Context, connection string, action linuxnative.AdminAction) (*linuxnative.Sender, error) {
	native, err := a.accountSender(ctx, connection, "")
	if err != nil {
		return nil, err
	}
	caps, err := native.Capabilities(ctx)
	if err != nil {
		return nil, err
	}
	if !GroupActionSupported(caps, action.Kind) {
		return nil, ErrGroupActionUnsupported
	}
	group, err := native.GroupProfile(ctx, action.ThreadID)
	if err != nil {
		return nil, err
	}
	if group.InGroup != nil && !*group.InGroup {
		return nil, ErrGroupActionTarget
	}
	current, err := a.SignedInAccount(ctx, connection)
	if err != nil {
		return nil, err
	}
	if current != native.Account() {
		return nil, ErrGroupActionTarget
	}
	return native, nil
}
