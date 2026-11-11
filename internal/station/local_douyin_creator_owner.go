package station

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/hostfile"
	"wechat-bot/internal/workspacefs"
)

// Bind the creator account to this saved browser profile and login epoch.
// Authorization changes may issue new observation grants, but they cannot
// silently replace the account that supplied the original works.
type localDouyinCreatorOwner struct {
	Version int    `json:"version"`
	Account string `json:"account"`
	Changed bool   `json:"changed,omitempty"`
}

func creatorOwnerLocation(a localDouyinWebAccess) (string, string) {
	return filepath.Dir(a.profile), "creator-owner-" + a.connection.AccountID + ".json"
}

func readCreatorOwner(root *os.Root, name string) (localDouyinCreatorOwner, error) {
	var owner localDouyinCreatorOwner
	file, err := workspacefs.OpenRegular(root, name)
	if err != nil {
		return owner, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 2049))
	if err != nil || len(raw) > 2048 || strictLocalMemoryDecode(raw, &owner) != nil || owner.Version != 1 ||
		(!owner.Changed || owner.Account != "") && !localDouyinPublicationHex(owner.Account) {
		return localDouyinCreatorOwner{}, apphost.ErrObservationWebSnapshotChanged
	}
	return owner, nil
}

func creatorOwnerCurrent(a localDouyinWebAccess, account string) bool {
	if account == "" {
		return false
	}
	owner, err := loadCreatorOwner(a)
	return err == nil && !owner.Changed && owner.Account == localCheckpointKey("douyin-creator-owner", account)
}

func loadCreatorOwner(a localDouyinWebAccess) (localDouyinCreatorOwner, error) {
	dir, name := creatorOwnerLocation(a)
	root, err := workspacefs.OpenPath(dir)
	if err != nil {
		return localDouyinCreatorOwner{}, err
	}
	defer root.Close()
	return readCreatorOwner(root, name)
}

// An empty account denotes a browser-verified account change, never an empty
// list or a network error. Keep that epoch invalid until explicit re-login.
func (p *localDouyinWebObservation) admitCreatorAccount(ctx context.Context, a localDouyinWebAccess, account string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, name := creatorOwnerLocation(a)
	root, err := workspacefs.OpenPath(dir)
	if err != nil {
		return apphost.ErrObservationWebStorageUnavailable
	}
	defer root.Close()
	unlock, err := hostfile.TryLock(filepath.Join(dir, name+".lock"))
	if err != nil {
		return apphost.ErrObservationWebSessionBusy
	}
	defer unlock()
	owner, err := readCreatorOwner(root, name)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return err
	}
	key := localCheckpointKey("douyin-creator-owner", account)
	if !missing && !owner.Changed && account != "" && owner.Account == key {
		return nil
	}
	if missing {
		owner = localDouyinCreatorOwner{Version: 1, Account: key, Changed: account == ""}
	} else {
		owner.Changed = true
	}
	raw, _ := json.Marshal(owner)
	if err := hostfile.WritePrivate(filepath.Join(dir, name), raw); err != nil {
		return apphost.ErrObservationWebStorageUnavailable
	}
	if owner.Changed {
		err := p.home.douyinWeb.captureMemory(ctx, a.record, douyinMemoryWorks, "", nil)
		return errors.Join(apphost.ErrObservationWebSnapshotChanged, err)
	}
	return nil
}
