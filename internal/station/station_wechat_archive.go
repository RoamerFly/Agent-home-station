package station

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	wechatapp "wechat-bot/connected-apps/wechat/adapter"
	"wechat-bot/internal/apphost"
	"wechat-bot/internal/stationhost"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
)

// This provider exposes only saved evidence to the local owner's routes.
// Deliberately do not embed stationWechat: it also implements native actions.
type stationWechatArchive struct {
	store *stationWechat
}

func attachStationWechatArchive(registry apphost.Registry, dataDir string, apps []stationhost.Attachment) (apphost.Registry, []stationhost.Attachment, error) {
	if _, attached := registry.Lookup(wechatapp.ID); attached {
		return registry, apps, nil
	}
	dir := filepath.Join(dataDir, "apps", wechatapp.ID, "workers")
	root, err := workspacefs.OpenPath(dir)
	if errors.Is(err, os.ErrNotExist) {
		return registry, apps, nil
	}
	if root != nil {
		root.Close()
	}
	store := &stationWechat{
		root: dir, dataDir: dataDir, installation: wechatapp.StationInstallationID,
		ctx: context.Background(), workers: map[string]*stationWechatWorker{},
	}
	entries := make([]apphost.Registration, 0, len(registry.Applications())+1)
	for _, app := range registry.Applications() {
		entry, _ := registry.Lookup(app.ID)
		entries = append(entries, entry)
	}
	entries = append(entries, apphost.Registration{
		Application: apphost.Application{ID: wechatapp.ID, Name: "微信", InstallationID: store.installation, Registration: "station-archive"},
		Provider:    &stationWechatArchive{store: store},
	})
	registry, err = apphost.New(entries...)
	if err != nil {
		return apphost.Registry{}, nil, err
	}
	apps = append(apps, stationhost.Attachment{Mount: func(owner *websession.Store, mux *http.ServeMux) {
		const base = "/api/apps/wechat/connections/{connection}/legacy-import"
		store.legacyReceiptRoutes(owner, mux, base+"/receipts")
		store.legacyRelocationReceiptRoute(owner, mux, base+"/relocation/receipts/{key}")
	}})
	return registry, apps, nil
}

func (a *stationWechatArchive) savedConnection(id string) (apphost.Connection, error) {
	_, account, err := a.store.deliveryCheckpoint(id)
	if errors.Is(err, apphost.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		archive, archiveErr := a.store.legacyReceiptArchive(id)
		account, err = archive.Plan.Account, archiveErr
	}
	if err != nil {
		return apphost.Connection{}, err
	}
	return apphost.Connection{ID: id, AccountID: account, Configuration: "archive-only"}, nil
}

func (a *stationWechatArchive) Connections(ctx context.Context) ([]apphost.Connection, error) {
	root, err := workspacefs.OpenPath(a.store.root)
	if err != nil {
		return nil, apphost.ErrUnavailable
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return nil, apphost.ErrUnavailable
	}
	defer dir.Close()
	items := []apphost.Connection{}
	rows, err := dir.ReadDir(-1)
	if err != nil {
		return nil, apphost.ErrUnavailable
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !row.IsDir() || !artifactID(row.Name(), "wechat-") {
			continue
		}
		if conn, err := a.savedConnection(row.Name()); err == nil {
			items = append(items, conn)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func (a *stationWechatArchive) Inspect(_ context.Context, id string) (apphost.ConnectionStatus, error) {
	conn, err := a.savedConnection(id)
	if err != nil {
		return apphost.ConnectionStatus{}, err
	}
	return apphost.ConnectionStatus{
		Connection: conn, Login: "not_checked", Runtime: "offline", Worker: "unavailable",
		SubmissionsPaused: true, CapabilityCheck: "not_checked", SupportedActions: []string{},
	}, nil
}

func (a *stationWechatArchive) ListReceipts(ctx context.Context, id, after string, limit int) (apphost.ReceiptPage, error) {
	if _, err := a.savedConnection(id); err != nil {
		return apphost.ReceiptPage{}, err
	}
	return a.store.ListReceipts(ctx, id, after, limit)
}

func (a *stationWechatArchive) ReadReceipt(ctx context.Context, id, key string) (apphost.Receipt, error) {
	return a.store.ReadReceipt(ctx, id, key)
}
