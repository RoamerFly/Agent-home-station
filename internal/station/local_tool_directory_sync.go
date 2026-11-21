package station

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"sort"

	"wechat-bot/internal/localasr"
	"wechat-bot/internal/pluginhost"
	catalog "wechat-bot/plugins"
)

// Directory tools are registered on startup and on catalogue refresh. This
// only prepares verified packages for on-demand use; it starts no tool process.
func (s *localProjectTools) refreshDirectoryTools(ctx context.Context, host *localProjectToolHost, sources map[string]discoveredToolPackage) map[string]string {
	s.update.Lock()
	defer s.update.Unlock()
	return s.syncDirectoryTools(ctx, host, sources)
}

func (s *localProjectTools) syncDirectoryTools(ctx context.Context, host *localProjectToolHost, sources map[string]discoveredToolPackage) map[string]string {
	notes := map[string]string{}
	if host == nil || s.home == nil || s.home.toolPackages == "" || s.options.CatalogDirectory == "" {
		return notes
	}
	ids := make([]string, 0, len(sources))
	for id := range sources {
		if id != "music" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var pending []pluginhost.InstalledPackage
	for _, id := range ids {
		source := sources[id]
		official, officialErr := catalog.ContractFor(id)
		item, err := host.store.Installed(ctx, id)
		if !source.compatible() && !(officialErr == nil && portableManagedTool(id) &&
			source.Runtime.Transport == "http-adapter" && source.hostCompatible() &&
			(localRVCSafeRegular(filepath.Join(source.directory, filepath.FromSlash(portableRegistrationFile))) ||
				err == nil && !item.Removed)) {
			continue
		}
		grants := source.Permissions
		if officialErr == nil {
			if source.digest != official.ManifestSHA256 {
				notes[id] = "工具包与当前工作站版本不匹配，已保留原版本。"
				continue
			}
			grants = officialProjectToolGrants(id, official.Package.Permissions)
		}
		if err == nil && item.Removed {
			continue
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			notes[id] = "工具记录暂不可读。"
			continue
		}
		previouslyEnabled := item.Enabled
		if errors.Is(err, sql.ErrNoRows) || item.Digest != source.digest {
			if item.Digest != "" && !subsetToolGrants(grants, item.Permissions) {
				notes[id] = "新版工具权限已变化，现有版本保留。"
				continue
			}
			prepared, e := host.store.PrepareCatalog(ctx, source.directory, source.digest)
			if e != nil || prepared.Package.ID != id || prepared.Digest != source.digest {
				notes[id] = "工具包校验未通过，检查是否已解压完整。"
				continue
			}
			if item.Digest != "" {
				busy, e := host.store.HasUnfinishedJobs(ctx, id)
				if e != nil || busy || s.isActive(id) || host.controller.Status(id).ActiveCalls > 0 {
					notes[id] = "新版已发现，等待在途任务结束后重载。"
					continue
				}
				if item.Enabled {
					// Use the same backend stop path as the desktop before replacing a package.
					if e = s.changeLifecycle(ctx, host, id, item.Revision, "stop", true); e != nil {
						notes[id] = "原版本尚未停止，等待下次刷新。"
						continue
					}
					item, e = host.store.SetEnabled(ctx, id, item.Revision, false)
					if e != nil {
						notes[id] = "原版本状态已经变化，请刷新。"
						continue
					}
				}
			}
			installed, e := host.store.Install(ctx, prepared.Token, prepared.Digest, grants)
			if e != nil {
				if previouslyEnabled {
					_, _ = host.store.SetEnabled(ctx, id, item.Revision, true)
				}
				notes[id] = "新版配置暂不兼容，已保留原工具。"
				continue
			}
			item = installed
		}
		if !item.Enabled && (previouslyEnabled || item.Revision == 1) {
			pending = append(pending, item)
		}
	}
	// Import selected shared settings before checking required configuration.
	s.importLegacyPublicToolConfig(ctx, host, localasr.PluginID)
	s.importLegacyPublicToolConfig(ctx, host, "web-search")
	for _, item := range pending {
		id := item.Package.ID
		if externalToolDataID(id) {
			if err := s.loadExternalToolConfig(ctx, host, item); err != nil {
				notes[id] = "工具等待配置，请检查该工具配置目录中的 config.json。"
				continue
			}
		}
		if id == localasr.PluginID && host.store.CheckConfiguration(ctx, id) != nil {
			notes[id] = "媒体后端等待配置。"
			continue
		}
		if id == localOBSPackage {
			if err := s.configurePortableOBS(ctx, host); err != nil {
				notes[id] = "便携 OBS 预览配置未就绪。"
				continue
			}
		}
		view, err := s.projectView(ctx, host, id, item.Package.Name, sources)
		if err != nil || !view.RuntimeReady {
			continue
		}
		if err = host.controller.EnableOnDemand(ctx, id, item.Revision); err != nil {
			notes[id] = "等待补齐工具设置或依赖。"
		}
	}
	return notes
}

func officialProjectToolGrants(id string, declared []string) []string {
	switch id {
	case localasr.PluginID:
		return []string{"artifact.read", "artifact.write", "model.invoke"}
	case localRVCPackage:
		return localRVCGrants
	case localComfyPackage:
		return localComfyGrants
	case localYuEPackage:
		return localYuEGrants
	case localTTSPackage:
		return localTTSGrants
	case localAudioCppPackage:
		return localAudioCppGrants
	case localOBSPackage:
		return []string{"network"}
	default:
		return declared
	}
}

func subsetToolGrants(requested, existing []string) bool {
	for _, grant := range requested {
		if !slices.Contains(existing, grant) {
			return false
		}
	}
	return true
}
