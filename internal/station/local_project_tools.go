package station

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"wechat-bot/internal/billing"
	"wechat-bot/internal/fishidentity"
	"wechat-bot/internal/localasr"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
	"wechat-bot/internal/stationruntime"
	"wechat-bot/internal/websession"
	"wechat-bot/internal/workspacefs"
	catalog "wechat-bot/plugins"
)

const localProjectToolIdle = 5 * time.Minute

type localProjectTools struct {
	legacyClientMu  sync.Mutex
	legacyClient    *legacyToolClient
	legacyMu        sync.Mutex
	legacyAt        time.Time
	legacyViews     map[string]localProjectToolView
	mcpMu           sync.Mutex
	mcpSessions     map[string]publicExternalMCPSession
	home            *localHomeService
	options         pluginruntime.Options
	mu              sync.Mutex
	update          sync.Mutex
	host            *localProjectToolHost
	closed          bool
	active          map[string]int
	rvc             localRVCBackend
	comfy           localComfyBackend
	yue             localYuEBackend
	tts             localTTSBackend
	audio           localAudioCppBackend
	obs             localOBSPreview
	obsProcesses    pluginruntime.ManagedServices
	ttsCancel       context.CancelFunc           // protected by update
	ttsJobID        string                       // protected by update
	visionPost      pluginruntime.RouteTransport // host-only transport; tests use an offline substitute
	visionUsage     *billing.Store
	visionUsageFish string
}

type localProjectToolHost struct {
	store             *pluginhost.Store
	controller        *pluginruntime.Controller
	comfyVideoPublish sync.Mutex
}

type localProjectToolView struct {
	DirectoryPackage   bool                         `json:"directory_package"`
	Backends           []pluginhost.ToolBackendView `json:"backends,omitempty"`
	PackageVersion     string                       `json:"package_version,omitempty"`
	PackageDigest      string                       `json:"package_digest,omitempty"`
	PackagePermissions []string                     `json:"package_permissions,omitempty"`
	ExternalPackage    bool                         `json:"external_package,omitempty"`
	Host               string                       `json:"host,omitempty"`
	ID                 string                       `json:"id"`
	Name               string                       `json:"name"`
	CatalogAvailable   bool                         `json:"catalog_available"`
	RuntimeReady       bool                         `json:"runtime_ready"`
	Busy               bool                         `json:"busy"`
	PDFReady           bool                         `json:"pdf_ready"`
	UpdateAvailable    bool                         `json:"update_available"`
	Installed          bool                         `json:"installed"`
	Enabled            bool                         `json:"enabled"`
	Revision           int                          `json:"revision"`
	State              string                       `json:"state"`
	Reason             string                       `json:"reason,omitempty"`
	VoiceProfiles      []localRVCVoice              `json:"voice_profiles,omitempty"`
	SpeechVoices       []localTTSVoice              `json:"speech_voices,omitempty"`
	AudioModels        []localAudioCppModelView     `json:"audio_models,omitempty"`
	Workflows          []localComfyWorkflowView     `json:"workflows,omitempty"`
}

func (s *localProjectTools) runtimeReady() bool {
	return runtime.GOOS == "windows" && stationruntime.ToolBrokerReady(s.options, s.home.root)
}

func (s *localProjectTools) pdfRuntimeReady() bool {
	if !s.runtimeReady() || s.options.DocumentRuntimeRoot == "" {
		return false
	}
	root, err := workspacefs.OpenPath(s.options.DocumentRuntimeRoot)
	if err != nil {
		return false
	}
	defer root.Close()
	file, err := workspacefs.OpenRegular(root, "runtime-manifest.json")
	if err != nil {
		return false
	}
	defer file.Close()
	var manifest struct {
		Version    int    `json:"schema_version"`
		Platform   string `json:"platform"`
		Policy     string `json:"policy"`
		Components struct {
			ReportLab string `json:"reportlab"`
			PDF       string `json:"pypdf"`
			Font      bool   `json:"embeddable_ttf"`
		} `json:"components"`
	}
	const limit = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	return err == nil && len(raw) <= limit && json.Unmarshal(raw, &manifest) == nil && manifest.Version == 2 &&
		manifest.Platform == "windows-x64" && manifest.Policy == "reportlab-offline-pypdf-reviewed-sources-v1" &&
		manifest.Components.ReportLab == "4.4.9-offline-patch-v1" && manifest.Components.PDF == "6.21.4" && manifest.Components.Font
}

func (s *localProjectTools) forFish(fish string) (*localProjectToolHost, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !fishidentity.ValidFishID(fish) {
		return nil, errors.New("本机项目工具已停止或身份不可用")
	}
	if s.host != nil {
		if s.host.store.Account() != fish {
			return nil, errors.New("本机身份已变化，请重启后使用项目工具")
		}
		return s.host, nil
	}
	dir := filepath.Join(stationPublicToolsDir(s.home.dataDir), "catalog")
	root, err := workspacefs.EnsurePath(dir)
	if err != nil {
		return nil, err
	}
	if err = root.Close(); err != nil {
		return nil, err
	}
	store, err := pluginhost.OpenStore(dir, fish)
	if err != nil {
		return nil, err
	}
	options := s.options
	options.DocumentRuntimeRoot = ""
	executor := stationruntime.InstalledToolExecutor(store, options, s.home.root)
	controller, err := pluginruntime.NewController(store, executor)
	if err != nil {
		store.Close()
		return nil, err
	}
	controller.SetIdleTimeout(localProjectToolIdle)
	s.host = &localProjectToolHost{store: store, controller: controller}
	return s.host, nil
}

func (s *localProjectTools) close() error {
	s.update.Lock()
	defer s.update.Unlock()
	s.mcpMu.Lock()
	for key, session := range s.mcpSessions {
		_ = session.hub.Close()
		delete(s.mcpSessions, key)
	}
	s.mcpMu.Unlock()
	s.obs.close()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	var err error
	obsCtx, obsCancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = s.obsProcesses.Close(obsCtx)
	obsCancel()
	if s.rvc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		err = s.rvc.Close(ctx)
		cancel()
	}
	if s.comfy != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		err = errors.Join(err, s.comfy.Close(ctx))
		cancel()
	}
	if s.yue != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		err = errors.Join(err, s.yue.Close(ctx))
		cancel()
	}
	if s.audio != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		err = errors.Join(err, s.audio.Close(ctx))
		cancel()
	}
	if s.host == nil {
		return err
	}
	err = errors.Join(err, s.host.controller.Close(), s.host.store.Close())
	s.host = nil
	return err
}

var localProjectPackages = []struct{ ID, Name string }{
	{pluginruntime.DocumentWorkshopID, "文档工坊"},
	{localAudioPackage, "音频工坊"},
	{localasr.PluginID, "媒体识别"},
	{localRVCPackage, "RVC 换声"},
	{localComfyPackage, "ComfyUI 创作"},
	{localYuEPackage, "YuE 作曲"},
	{localTTSPackage, "本机语音"},
	{localAudioCppPackage, "audio.cpp 语音引擎"},
	{localOBSPackage, "OBS 本地预览"},
}

func (s *localProjectTools) markActive(id string) func() {
	s.mu.Lock()
	if s.active == nil {
		s.active = make(map[string]int)
	}
	s.active[id]++
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.active[id]--
		s.mu.Unlock()
	}
}

func (s *localProjectTools) isActive(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[id] > 0
}

func (s *localProjectTools) projectView(ctx context.Context, host *localProjectToolHost, id, name string, discovered ...map[string]discoveredToolPackage) (view localProjectToolView, err error) {
	view = localProjectToolView{ID: id, Name: name, RuntimeReady: s.runtimeReady(), State: "not_installed"}
	var sources map[string]discoveredToolPackage
	if len(discovered) > 0 {
		sources = discovered[0]
	} else {
		sources = discoverToolPackages(s.options.CatalogDirectory)
	}
	defer func() {
		_, view.DirectoryPackage = sources[id]
		if err == nil {
			s.populateToolBackends(ctx, host, &view)
		}
	}()
	// A catalog migration placeholder is not an executable tool. Keep it visible
	// with an actionable reason instead of reporting a ready Python runtime.
	contract, contractErr := catalog.ContractFor(id)
	if contractErr == nil && contract.Package.Runtime.Transport == "unbound" {
		view.RuntimeReady = false
		view.Reason = "该工具包尚未绑定执行后端。"
		if id == "visual-generation" {
			view.Reason = "本机图像和视频生成使用「ComfyUI 创作」中已登记的工作流。"
		}
		view.State = "not_available"
		return view, nil
	}

	var backendBusy bool
	var backendRunning bool
	if s.options.CatalogDirectory != "" {
		_, err := catalogPackageDir(s.options.CatalogDirectory, view.ID)
		view.CatalogAvailable = err == nil
	}
	if id == localRVCPackage {
		view.RuntimeReady = false
		if host != nil && s.rvc != nil {
			info, err := s.rvc.Info(ctx, host.store.Account())
			view.RuntimeReady, view.VoiceProfiles = err == nil && len(info.Profiles) != 0, info.Profiles
			backendBusy = err == nil && info.Busy
			backendRunning = err == nil && info.Running
			if err != nil {
				view.Reason = localRVCSetupReason(err)
			} else if !view.RuntimeReady {
				view.Reason = "尚未登记获准使用的 RVC 声线。"
			}
		}
	}
	if id == localComfyPackage {
		view.RuntimeReady = false
		view.Reason = "comfyui_backend_not_configured"
		if host != nil && s.comfy != nil {
			info, workflows, err := s.comfySetup(ctx, host.store.Account())
			view.RuntimeReady = err == nil
			backendRunning = err == nil && info.Running
			backendBusy = err == nil && info.Busy
			view.Workflows = localComfyWorkflowViews(workflows)
			if err == nil {
				view.Reason = ""
			} else if errors.Is(err, errLocalComfyWorkflowSetup) {
				view.Reason = "comfyui_workflows_missing"
			} else if err != nil {
				view.Reason = err.Error() // comfySetup returns only public setup messages.
			}
		}
	}
	if id == localYuEPackage {
		view.RuntimeReady = false
		view.Reason = "yue_backend_not_configured"
		if host != nil && s.yue != nil {
			info, err := s.yue.Info(ctx, host.store.Account())
			if err == nil {
				view.RuntimeReady, backendBusy, backendRunning, view.Reason = true, info.Busy, info.Running, ""
			} else {
				view.Reason = localComputeSetupReason(localYuEPackage, err)
			}
		}
	}
	if id == localTTSPackage {
		view.RuntimeReady = false
		view.Reason = "station_speech_backend_not_configured"
		if host != nil && s.tts != nil {
			info, err := s.tts.Info(ctx, host.store.Account())
			if err == nil && len(info.Voices) > 0 {
				view.RuntimeReady, view.Reason, view.SpeechVoices = true, "", info.Voices
			} else {
				view.Reason = localTTSSetupReason(err)
			}
		}
	}
	if id == localAudioCppPackage {
		view.RuntimeReady = false
		view.Reason = "audio_cpp_not_installed_or_authorized"
		if host != nil && s.audio != nil {
			info, checkErr := s.audio.Info(ctx, host.store.Account())
			view.AudioModels = info.Models
			if checkErr == nil && len(info.Models) > 0 {
				ready := true
				for _, model := range info.Models {
					if (model.ID == info.TTSModelID || model.ID == info.ASRModelID) && !model.Configured {
						ready = false
					}
				}
				view.RuntimeReady, view.Reason, backendBusy = ready, "", info.Busy
				backendRunning = info.Running
				if !ready {
					view.Reason = "audio_cpp_model_missing"
				}
			} else if checkErr != nil {
				view.Reason = checkErr.Error()
			}
		}
	}
	if id == localOBSPackage {
		view.RuntimeReady = false
		view.Reason = "obs_preview_not_configured"
		if host != nil {
			if config, err := localOBSConfiguration(ctx, host); err == nil && config.Configured {
				view.RuntimeReady = true
				view.Reason = ""
				backendBusy = s.obs.status(config).Active
				backendRunning = s.obsProcesses.Status("preview").Running
			}
		}
	}
	if contractErr != nil {
		if source, ok := sources[id]; ok {
			view.ExternalPackage = true
			view.PackageVersion, view.PackageDigest = source.Version, source.digest
			view.PackagePermissions = source.Permissions
			view.CatalogAvailable = source.compatible()
			if !view.CatalogAvailable {
				view.State = "not_available"
				view.Reason = "此工具包的运行协议、入口或平台与当前工作站不兼容。"
			}
		}
	}
	if host == nil {
		return view, nil
	}
	item, err := host.store.Installed(ctx, view.ID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && item.Removed {
		return view, nil
	}
	if err != nil {
		return view, err
	}
	view.Installed, view.Enabled, view.Revision = true, item.Enabled, item.Revision
	if id == localasr.PluginID && item.Enabled {
		if _, err := s.home.localStationASRVersion(ctx); err != nil {
			if _, visionErr := s.home.localStationVisionVersion(ctx); visionErr != nil {
				view.RuntimeReady = false
				view.Reason = "本机媒体资源未就绪，请核对语音识别资源或配置视觉服务。"
			}
		}
	}
	targetDigest := contract.ManifestSHA256
	if contractErr != nil {
		view.CatalogAvailable = view.ExternalPackage && view.CatalogAvailable
		view.ExternalPackage = true
		if view.PackageVersion == "" {
			view.PackageVersion = item.Package.Version
			view.PackagePermissions = item.Permissions
		}
		targetDigest = view.PackageDigest
		view.Reason = ""
	}
	if item.Enabled && item.Package.Runtime.Transport == "mcp-stdio" && runtime.GOOS == "windows" {
		// InstalledToolExecutor selects a package-owned interpreter when present.
		// The shared release interpreter is not a prerequisite for that package.
		options := s.options
		options.DocumentRuntimeRoot = ""
		grant := pluginhost.Grant{Account: host.store.Account(), Plugin: item.Package.ID, Version: item.Package.Version, Permissions: item.Permissions}
		_, resolveErr := stationruntime.InstalledToolExecutor(host.store, options, s.home.root).Resolve(ctx, item.Package, grant)
		view.RuntimeReady = resolveErr == nil
		if resolveErr != nil {
			view.Reason = "tool_runtime_unavailable"
		}
	}
	view.UpdateAvailable = view.CatalogAvailable && item.Digest != targetDigest
	view.PDFReady = id == pluginruntime.DocumentWorkshopID && (item.Digest == contract.ManifestSHA256 || compatibleDocumentDigest(projectOperation(localDocumentExportTool), item.Digest)) && s.pdfRuntimeReady()
	view.Busy = backendBusy || s.isActive(id)
	switch {
	case !item.Enabled:
		view.State = "disabled"
	case !view.RuntimeReady:
		view.State = "runtime_missing"
	case view.Busy || backendRunning:
		view.State = "running"
	default:
		view.State = host.controller.Status(view.ID).State
		if view.State == "stopped" {
			view.State = "idle"
		}
	}
	return view, nil
}

func (s *localProjectTools) packages(ctx context.Context, host *localProjectToolHost, sources map[string]discoveredToolPackage) []struct{ ID, Name string } {
	out := append([]struct{ ID, Name string }{}, localProjectPackages...)
	seen := map[string]bool{}
	for _, p := range out {
		seen[p.ID] = true
	}
	if items, err := catalog.List(); err == nil {
		for _, p := range items {
			if p.ID != "mobile-control" && p.ID != "music" && !seen[p.ID] {
				out = append(out, struct{ ID, Name string }{p.ID, p.Name})
				seen[p.ID] = true
			}
		}
	}
	if host != nil {
		if items, err := host.store.List(ctx); err == nil {
			for _, p := range items {
				if !p.Removed && p.Package.ID != "mobile-control" && p.Package.ID != "music" && !seen[p.Package.ID] {
					out = append(out, struct{ ID, Name string }{p.Package.ID, p.Package.Name})
					seen[p.Package.ID] = true
				}
			}
		}
	}
	ids := make([]string, 0, len(sources))
	for id := range sources {
		if id != "music" && !seen[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		out = append(out, struct{ ID, Name string }{id, sources[id].Name})
	}
	return out
}

func (s *localProjectTools) routes(owner *websession.Store, mux *http.ServeMux) {
	s.toolFileRoutes(owner, mux)
	mux.HandleFunc("POST /api/local/project-tools/{id}/balance", owner.Protect(s.publicToolBalance))
	s.visionConfigurationRoutes(owner, mux)
	mux.HandleFunc("GET "+sharedASRRoute, owner.Protect(s.sharedASR))
	mux.HandleFunc("POST "+sharedASRRoute, owner.Protect(s.sharedASR))
	mux.HandleFunc("GET "+sharedSpeechRoute, owner.Protect(s.sharedSpeech))
	mux.HandleFunc("POST "+sharedSpeechRoute, owner.Protect(s.sharedSpeech))
	s.comfyDeploymentRoutes(owner, mux)
	s.yueDeploymentRoutes(owner, mux)
	s.audioCppDeploymentRoutes(owner, mux)
	mux.HandleFunc("GET /api/local/project-tools/applications", owner.Protect(func(w http.ResponseWriter, r *http.Request) {
		rows := make([]localProjectToolView, 0)
		for _, v := range s.legacyToolViews(r.Context()) {
			rows = append(rows, v)
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		w.Header().Set("Cache-Control", "no-store")
		websession.Respond(w, 200, map[string]any{"wechat": rows})
	}))
	mux.HandleFunc("GET /api/local/project-tools", owner.Protect(func(w http.ResponseWriter, r *http.Request) {
		record, err := s.home.identity.Read()
		if err != nil {
			websession.Fail(w, 409, "本机身份记录不可读")
			return
		}
		var host *localProjectToolHost
		if record.LocalOwner != nil {
			host, err = s.forFish(record.FishID)
			if err != nil {
				websession.Fail(w, 503, "项目工具安装状态不可读")
				return
			}
		}
		views := make([]localProjectToolView, 0, len(localProjectPackages))
		sources := discoverToolPackages(s.options.CatalogDirectory)
		notes := s.refreshDirectoryTools(r.Context(), host, sources)
		for _, pkg := range s.packages(r.Context(), host, sources) {
			view, err := s.projectView(r.Context(), host, pkg.ID, pkg.Name, sources)
			if err != nil {
				websession.Fail(w, 503, "项目工具安装状态不可读")
				return
			}
			if note := notes[pkg.ID]; note != "" {
				view.Reason = note
			}
			views = append(views, view)
		}
		w.Header().Set("Cache-Control", "no-store")
		websession.Respond(w, 200, map[string]any{"tools": views})
	}))
	mux.HandleFunc("PUT /api/local/project-tools/{id}", owner.Protect(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		record, err := s.home.identity.Read()
		if err != nil {
			websession.Fail(w, 409, "身份不可读")
			return
		}
		host, err := s.forFish(record.FishID)
		if err != nil {
			websession.Fail(w, 503, "工具目录不可读")
			return
		}
		sources := discoverToolPackages(s.options.CatalogDirectory)
		for _, p := range s.packages(r.Context(), host, sources) {
			if p.ID == id {
				s.changeProjectTool(w, r, id, p.Name)
				return
			}
		}
		websession.Fail(w, 404, "工具未登记")
	}))
	preview := owner.Protect(func(w http.ResponseWriter, r *http.Request) {
		s.obsPreview(owner, w, r)
	})
	mux.HandleFunc("GET /api/local/project-tools/"+localOBSPackage+"/preview", preview)
	mux.HandleFunc("POST /api/local/project-tools/"+localOBSPackage+"/preview", preview)
}

func (s *localProjectTools) changeProjectTool(w http.ResponseWriter, r *http.Request, id, name string) {
	var in struct {
		Action           string `json:"action"`
		Revision         int    `json:"revision"`
		ApproveArtifacts bool   `json:"approve_artifacts"`
		PackageDigest    string `json:"package_digest"`
		IdleOnly         bool   `json:"idle_only"`
	}
	if !websession.Decode(w, r, &in) {
		return
	}
	if in.Action != "install" && in.Action != "upgrade" && in.Action != "enable" && in.Action != "disable" && in.Action != "start" && in.Action != "stop" && in.Action != "restart" && in.Action != "ensure_ready" && in.Action != "repair" {
		websession.Fail(w, 400, "未知的项目工具操作")
		return
	}
	if (in.Action == "install" || in.Action == "upgrade") && !in.ApproveArtifacts {
		if id == localOBSPackage {
			websession.Fail(w, 400, "安装 OBS 预览需要主人确认访问本机 WebSocket 的权限")
			return
		}
		if id == localComfyPackage {
			websession.Fail(w, 400, "安装 ComfyUI 需要主人确认本机成品写入与计算权限")
			return
		}
		if id == localRVCPackage || id == localYuEPackage || id == localTTSPackage {
			websession.Fail(w, 400, "安装需要主人确认任务素材读写与本机计算权限")
			return
		}
		websession.Fail(w, 400, "安装需要主人明确授予本机成品读写权限")
		return
	}
	s.update.Lock()
	defer s.update.Unlock()
	ctx := r.Context()
	record, err := s.home.identity.Read()
	if err != nil || record.LocalOwner == nil {
		websession.Fail(w, 409, "请先准备本机主人身份")
		return
	}
	host, err := s.forFish(record.FishID)
	if err != nil {
		websession.Fail(w, 503, "项目工具安装状态不可读")
		return
	}
	current, err := host.store.Installed(ctx, id)
	if in.Action == "install" || in.Action == "upgrade" {
		missing := errors.Is(err, sql.ErrNoRows) || err == nil && current.Removed
		if in.Action == "upgrade" && (err != nil || current.Removed || current.Enabled || current.Revision != in.Revision) {
			websession.Fail(w, 409, "请先停用该工具并刷新版本，再确认更新")
			return
		}
		if err != nil && !missing {
			websession.Fail(w, 503, "项目工具安装状态不可读")
			return
		}
		contract, contractErr := catalog.ContractFor(id)
		if contractErr != nil {
			source, ok := discoverToolPackages(s.options.CatalogDirectory)[id]
			if !ok || !source.compatible() || in.PackageDigest != source.digest {
				websession.Fail(w, 409, "工具包未就绪或版本已变化，请刷新并重新确认")
				return
			}
			if in.Action == "install" && !missing {
				websession.Fail(w, 409, "工具已安装，请使用更新操作")
				return
			}
			prepared, prepareErr := host.store.PrepareCatalog(ctx, source.directory, source.digest)
			if prepareErr != nil || prepared.Package.ID != id || prepared.Digest != source.digest {
				websession.Fail(w, 409, "工具包校验失败，请检查解压是否完整以及清单内的文件摘要")
				return
			}
			_, err = host.store.Install(ctx, prepared.Token, prepared.Digest, prepared.Package.Permissions)
		} else if in.Action == "install" && !missing && current.Digest != contract.ManifestSHA256 {
			websession.Fail(w, 409, "已安装版本需要单独升级")
			return
		} else if missing || current.Digest != contract.ManifestSHA256 {
			if s.options.CatalogDirectory == "" {
				websession.Fail(w, 409, "当前发行包未提供该项目工具")
				return
			}
			packageDir, resolveErr := catalogPackageDir(s.options.CatalogDirectory, id)
			if resolveErr != nil {
				websession.Fail(w, 409, "项目工具发行源缺失或摘要不匹配")
				return
			}
			prepared, prepareErr := host.store.PrepareCatalog(ctx, packageDir, contract.ManifestSHA256)
			if prepareErr != nil || prepared.Package.ID != id || prepared.Digest != contract.ManifestSHA256 {
				websession.Fail(w, 409, "项目工具发行源缺失或摘要不匹配")
				return
			}
			grants := officialProjectToolGrants(id, contract.Package.Permissions)
			_, err = host.store.Install(ctx, prepared.Token, prepared.Digest, grants)
		}
	} else {
		if err != nil || current.Removed || current.Revision != in.Revision {
			websession.Fail(w, 409, "项目工具版本已变化，请刷新后重试")
			return
		}
		if in.Action == "ensure_ready" || in.Action == "repair" {
			view, viewErr := s.projectView(ctx, host, id, name)
			if viewErr != nil {
				websession.Fail(w, 503, "项目工具状态暂不可读")
				return
			}
			state, recoverErr := s.ensureProjectToolReady(ctx, host, view, in.Action == "repair")
			if recoverErr == nil && (state == "ready" || state == "restarted") {
				view, viewErr = s.projectView(ctx, host, id, name)
				if viewErr != nil {
					websession.Fail(w, 503, "项目工具恢复后状态暂不可读")
					return
				}
			}
			w.Header().Set("Cache-Control", "no-store")
			websession.Respond(w, 200, projectToolRepairResult(view, state, recoverErr))
			return
		}
		if (in.Action == "start" || in.Action == "stop" || in.Action == "restart") && !current.Enabled {
			websession.Fail(w, 409, "请先启用该项目工具")
			return
		}
		if externalToolDataID(id) && (in.Action == "enable" || in.Action == "start" || in.Action == "restart") {
			if err := s.loadExternalToolConfig(ctx, host, current); err != nil {
				websession.Fail(w, 409, "工具配置暂未加载，请检查该工具配置目录中的 config.json 必填项和格式，或等待在途任务结束。已有配置保留。")
				return
			}
		}
		if in.Action == "start" || in.Action == "stop" || in.Action == "restart" {
			err = s.changeLifecycle(ctx, host, id, current.Revision, in.Action, in.IdleOnly)
		} else if id == localOBSPackage {
			if in.Action == "enable" {
				config, checkErr := localOBSConfiguration(ctx, host)
				if checkErr != nil || !config.Configured {
					websession.Fail(w, 409, "请先保存 OBS 本机端口和 WebSocket 密码")
					return
				}
			}
			if in.Action == "disable" {
				if err := s.stopOBSProcess(ctx); err != nil {
					websession.Fail(w, 409, err.Error())
					return
				}
			}
			_, err = host.store.SetEnabled(ctx, id, current.Revision, in.Action == "enable")
		} else if id == localAudioCppPackage {
			if in.Action == "enable" || in.Action == "start" || in.Action == "restart" {
				if s.audio == nil {
					websession.Fail(w, 409, "audio.cpp 本机后端未登记")
					return
				}
				if in.Action == "enable" {
					if _, checkErr := s.audio.Info(ctx, record.FishID); checkErr != nil {
						websession.Fail(w, 409, "请先登记并核对 audio.cpp 的可用模型")
						return
					}
					if _, err = host.store.SetEnabled(ctx, id, current.Revision, true); err != nil {
						websession.Fail(w, 409, "audio.cpp 工具启用状态未保存")
						return
					}
				}
				if in.Action == "restart" {
					stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 25*time.Second)
					_ = s.audio.Stop(stopCtx)
					cancel()
				}
				if in.Action == "enable" || in.Action == "start" || in.Action == "restart" {
					err = s.audio.Start(ctx, record.FishID)
				}
			} else if in.Action == "stop" {
				err = s.audio.Stop(ctx)
			} else {
				_, err = host.store.SetEnabled(ctx, id, current.Revision, false)
				if err == nil {
					stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 25*time.Second)
					err = s.audio.Stop(stopCtx)
					cancel()
				}
			}
		} else if id == localTTSPackage {
			if in.Action == "enable" {
				if s.tts == nil {
					websession.Fail(w, 409, "本机语音后端未登记")
					return
				}
				if info, checkErr := s.tts.Info(ctx, record.FishID); checkErr != nil || len(info.Voices) == 0 {
					websession.Fail(w, 409, localTTSSetupReason(checkErr))
					return
				}
			}
			_, err = host.store.SetEnabled(ctx, id, current.Revision, in.Action == "enable")
			if err == nil && in.Action == "disable" && s.ttsCancel != nil {
				s.ttsCancel()
			}
		} else if id == localYuEPackage {
			if in.Action == "enable" {
				if s.yue == nil {
					websession.Fail(w, 409, "本机 YuE 后端未登记")
					return
				}
				if _, err := s.yue.Info(ctx, record.FishID); err != nil {
					websession.Fail(w, 409, localComputeSetupReason(localYuEPackage, err))
					return
				}
			}
			if in.Action == "enable" {
				_, err = host.store.SetEnabled(ctx, id, current.Revision, true)
			} else if in.Action == "disable" {
				_, err = host.store.SetEnabled(ctx, id, current.Revision, false)
			} else if in.Action == "stop" {
				err = s.yue.Stop(ctx)
			} else if in.Action == "start" || in.Action == "restart" {
				info, infoErr := s.yue.Info(ctx, record.FishID)
				if infoErr != nil {
					err = infoErr
				} else {
					if in.Action == "restart" {
						_ = s.yue.Stop(ctx)
					}
					err = s.yue.Use(ctx, record.FishID, info.Binding, true, func(localComfyAudioClient) error { return nil })
				}
			}
			if err == nil && in.Action == "disable" && s.yue != nil {
				stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 25*time.Second)
				err = s.yue.Stop(stopCtx)
				cancel()
				if err != nil {
					websession.Fail(w, 503, "YuE 已停用，后端退出尚待核对")
					return
				}
			}
		} else if id == localComfyPackage {
			if in.Action == "enable" {
				if _, _, checkErr := s.comfySetup(ctx, record.FishID); checkErr != nil {
					websession.Fail(w, 409, checkErr.Error())
					return
				}
			}
			_, err = host.store.SetEnabled(ctx, id, current.Revision, in.Action == "enable")
			if err == nil && in.Action == "disable" && s.comfy != nil {
				stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 25*time.Second)
				err = s.comfy.Stop(stopCtx)
				cancel()
				if err != nil {
					websession.Fail(w, 503, "ComfyUI 已停用，新任务已拦截，正在核对后端退出。")
					return
				}
			}
		} else if id == localRVCPackage {
			if in.Action == "enable" {
				if s.rvc == nil {
					websession.Fail(w, 409, "当前发行包未提供 RVC 后端")
					return
				}
				info, checkErr := s.rvc.Info(ctx, record.FishID)
				if checkErr != nil || len(info.Profiles) == 0 {
					websession.Fail(w, 409, localRVCSetupReason(checkErr))
					return
				}
			}
			if in.Action == "enable" {
				_, err = host.store.SetEnabled(ctx, id, current.Revision, true)
			} else if in.Action == "disable" {
				_, err = host.store.SetEnabled(ctx, id, current.Revision, false)
			} else if in.Action == "stop" {
				err = s.rvc.Stop(ctx)
			} else if in.Action == "start" || in.Action == "restart" {
				info, infoErr := s.rvc.Info(ctx, record.FishID)
				if infoErr != nil || len(info.Profiles) == 0 {
					err = infoErr
					if err == nil {
						err = errors.New("station_rvc_profile_not_registered")
					}
				} else {
					if in.Action == "restart" {
						_ = s.rvc.Stop(ctx)
					}
					profile := info.Profiles[0].ID
					_, _, err = s.rvc.Call(ctx, record.FishID, info.Binding, http.MethodGet, "/rvc-health?voice_profile_id="+url.QueryEscape(profile), "", nil)
				}
			}
			if err == nil && in.Action == "disable" && s.rvc != nil {
				stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 25*time.Second)
				err = s.rvc.Stop(stopCtx)
				cancel()
				if err != nil {
					websession.Fail(w, 503, "RVC 已停用，新任务已拦截，正在核对后端退出。")
					return
				}
			}
		} else if id == localasr.PluginID {
			if in.Action == "enable" {
				if !current.Enabled {
					if err = s.configureStationASR(ctx, host); err != nil {
						config, configErr := host.store.Configuration(ctx, localasr.PluginID)
						if configErr != nil || !stationVisionConfigured(config) {
							websession.Fail(w, 409, "媒体理解尚未配置视觉服务或可用的本机识别资源")
							return
						}
					}
				}
				err = host.controller.EnableOnDemand(ctx, id, current.Revision)
			} else {
				err = host.controller.Stop(ctx, id, current.Revision)
			}
		} else if in.Action == "enable" {
			err = host.controller.EnableOnDemand(ctx, id, current.Revision)
		} else {
			err = host.controller.Stop(ctx, id, current.Revision)
		}
	}
	if err != nil {
		websession.Fail(w, 409, "项目工具设置未保存，请核对版本、依赖和运行中的任务")
		return
	}
	view, err := s.projectView(ctx, host, id, name)
	if err != nil {
		websession.Fail(w, 503, "项目工具状态暂不可读")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	websession.Respond(w, 200, map[string]any{"tool": view})
}
