package admin

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"wechat-bot/internal/pluginhost"
	catalog "wechat-bot/plugins"
)

// Only the pinned WeChat adapter tools belong in this application catalogue.
// Public packages are reconciled by the Station host instead.
func (m *Manager) ensureWechatDirectoryTools(ctx context.Context, store *pluginhost.Store) {
	if _, shared := m.sharedStationVision(); !shared || m.draining() {
		return
	}
	options, err := runtimeDeploymentOptions()
	if err != nil || options.CatalogDirectory == "" {
		return
	}
	m.op.Lock()
	defer m.op.Unlock()
	for _, id := range []string{"share-reader", "music"} {
		item, err := store.Installed(ctx, id)
		if !errors.Is(err, sql.ErrNoRows) {
			continue
		}
		official, err := catalog.ContractFor(id)
		if err != nil {
			continue
		}
		entries, err := os.ReadDir(options.CatalogDirectory)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || (entry.Name() != id && !strings.HasPrefix(entry.Name(), id+"-")) {
				continue
			}
			prepared, err := store.PrepareCatalog(ctx, filepath.Join(options.CatalogDirectory, entry.Name()), official.ManifestSHA256)
			if err != nil || prepared.Package.ID != id {
				continue
			}
			item, err = store.Install(ctx, prepared.Token, prepared.Digest, official.Package.Permissions)
			if err == nil && store.CheckConfiguration(ctx, id) == nil {
				if h, ready, e := m.installedPluginRuntime(); e == nil && ready {
					_ = h.EnableOnDemand(ctx, id, item.Revision)
				}
			}
			break
		}
	}
}
