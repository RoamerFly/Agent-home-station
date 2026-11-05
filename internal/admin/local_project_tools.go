package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
	"wechat-bot/internal/workspacefs"
	catalog "wechat-bot/plugins"
)

const localProjectToolIdle = 5 * time.Minute

type localProjectTools struct {
	home      *localHomeService
	options   pluginruntime.Options
	mu        sync.Mutex
	update    sync.Mutex
	host      *localProjectToolHost
	closed    bool
	active    map[string]int
	rvc       localRVCBackend
	comfy     localComfyBackend
	yue       localYuEBackend
	tts       localTTSBackend
	ttsCancel context.CancelFunc // protected by update
}

type localProjectToolHost struct {
	store      *pluginhost.Store
	controller *pluginruntime.Controller
}

type localProjectToolView struct {
	ID               string                   `json:"id"`
	Name             string                   `json:"name"`
	CatalogAvailable bool                     `json:"catalog_available"`
	RuntimeReady     bool                     `json:"runtime_ready"`
	PDFReady         bool                     `json:"pdf_ready"`
	UpdateAvailable  bool                     `json:"update_available"`
	Installed        bool                     `json:"installed"`
	Enabled          bool                     `json:"enabled"`
	Revision         int                      `json:"revision"`
	State            string                   `json:"state"`
	Reason           string                   `json:"reason,omitempty"`
	VoiceProfiles    []localRVCVoice          `json:"voice_profiles,omitempty"`
	SpeechVoices     []localTTSVoice          `json:"speech_voices,omitempty"`
	Workflows        []localComfyWorkflowView `json:"workflows,omitempty"`
}

func (s *localProjectTools) runtimeReady() bool {
	return runtime.GOOS == "windows" && windowsToolRuntimeReady(s.options)
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
	if s.closed || !validFishID(fish) {
		return nil, errors.New("本机项目工具已停止或身份不可用")
	}
	if s.host != nil {
		if s.host.store.Account() != fish {
			return nil, errors.New("本机身份已变化，请重启后使用项目工具")
		}
		return s.host, nil
	}
	dir := filepath.Join(s.home.dataDir, "project-tools", fish)
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
	controller, err := pluginruntime.NewController(store, pluginruntime.InstalledExecutor(store, options))
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	var err error
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
	if s.host == nil {
		return err
	}
	err = errors.Join(err, s.host.controller.Close(), s.host.store.Close())
	s.host = nil
	return err
}

var localProjectPackages = []struct{ ID, Name string }{
	{documentWorkshopPluginID, "文档工坊"},
	{localAudioPackage, "音频工坊"},
	{localRVCPackage, "RVC 换声"},
	{localComfyPackage, "ComfyUI 创作"},
	{localYuEPackage, "YuE 作曲"},
	{localTTSPackage, "本机语音"},
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

func (s *localProjectTools) projectView(ctx context.Context, host *localProjectToolHost, id, name string) (localProjectToolView, error) {
	view := localProjectToolView{ID: id, Name: name, RuntimeReady: s.runtimeReady(), State: "not_installed"}
	var backendBusy bool
	if s.options.CatalogDirectory != "" {
		info, err := os.Lstat(filepath.Join(s.options.CatalogDirectory, view.ID, "plugin.json"))
		view.CatalogAvailable = err == nil && info.Mode().IsRegular()
	}
	if id == localRVCPackage {
		view.RuntimeReady = false
		if host != nil && s.rvc != nil {
			info, err := s.rvc.Info(ctx, host.store.Account())
			view.RuntimeReady, view.VoiceProfiles = err == nil && len(info.Profiles) != 0, info.Profiles
			backendBusy = err == nil && info.Busy
			if err != nil {
				view.Reason = localRVCSetupReason(err)
			} else if !view.RuntimeReady {
				view.Reason = "尚未登记获准使用的 RVC 声线。"
			}
		}
	}
	if id == localComfyPackage {
		view.RuntimeReady = false
		if host != nil && s.comfy != nil {
			info, workflows, err := s.comfySetup(ctx, host.store.Account())
			view.RuntimeReady = err == nil
			backendBusy = err == nil && info.Busy
			view.Workflows = localComfyWorkflowViews(workflows)
			if errors.Is(err, errLocalComfyWorkflowSetup) {
				view.Reason = "comfyui_workflows_missing"
			} else if err != nil {
				view.Reason = "comfyui_backend_not_configured"
			}
		}
	}
	if id == localYuEPackage {
		view.RuntimeReady = false
		view.Reason = "yue_backend_not_configured"
		if host != nil && s.yue != nil {
			info, err := s.yue.Info(ctx, host.store.Account())
			if err == nil {
				view.RuntimeReady, backendBusy, view.Reason = true, info.Busy, ""
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
	contract, err := catalog.ContractFor(id)
	if err != nil {
		return view, err
	}
	view.UpdateAvailable = view.CatalogAvailable && item.Digest != contract.ManifestSHA256
	view.PDFReady = id == documentWorkshopPluginID && item.Digest == contract.ManifestSHA256 && s.pdfRuntimeReady()
	switch {
	case !item.Enabled:
		view.State = "disabled"
	case !view.RuntimeReady:
		view.State = "runtime_missing"
	case backendBusy || s.isActive(id):
		view.State = "running"
	default:
		view.State = host.controller.Status(view.ID).State
		if view.State == "stopped" {
			view.State = "idle"
		}
	}
	return view, nil
}

func (s *localProjectTools) routes(owner *Server, mux *http.ServeMux) {
	mux.HandleFunc("GET /api/local/project-tools", owner.protected(func(w http.ResponseWriter, r *http.Request) {
		record, err := s.home.identity.localIdentity()
		if err != nil {
			fail(w, 409, "本机身份记录不可读")
			return
		}
		var host *localProjectToolHost
		if record.LocalOwner != nil {
			host, err = s.forFish(record.FishID)
			if err != nil {
				fail(w, 503, "项目工具安装状态不可读")
				return
			}
		}
		views := make([]localProjectToolView, 0, len(localProjectPackages))
		for _, pkg := range localProjectPackages {
			view, err := s.projectView(r.Context(), host, pkg.ID, pkg.Name)
			if err != nil {
				fail(w, 503, "项目工具安装状态不可读")
				return
			}
			views = append(views, view)
		}
		w.Header().Set("Cache-Control", "no-store")
		respond(w, 200, map[string]any{"tools": views})
	}))
	for _, pkg := range localProjectPackages {
		mux.HandleFunc("PUT /api/local/project-tools/"+pkg.ID, owner.protected(func(w http.ResponseWriter, r *http.Request) {
			s.changeProjectTool(w, r, pkg.ID, pkg.Name)
		}))
	}
}

func (s *localProjectTools) changeProjectTool(w http.ResponseWriter, r *http.Request, id, name string) {
	var in struct {
		Action           string `json:"action"`
		Revision         int    `json:"revision"`
		ApproveArtifacts bool   `json:"approve_artifacts"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Action != "install" && in.Action != "upgrade" && in.Action != "enable" && in.Action != "disable" {
		fail(w, 400, "未知的项目工具操作")
		return
	}
	if (in.Action == "install" || in.Action == "upgrade") && !in.ApproveArtifacts {
		if id == localComfyPackage {
			fail(w, 400, "安装 ComfyUI 需要主人确认本机成品写入与计算权限")
			return
		}
		if id == localRVCPackage || id == localYuEPackage || id == localTTSPackage {
			fail(w, 400, "安装需要主人确认任务素材读写与本机计算权限")
			return
		}
		fail(w, 400, "安装需要主人明确授予本机成品读写权限")
		return
	}
	if in.Action == "enable" && id != localRVCPackage && id != localComfyPackage && id != localYuEPackage && id != localTTSPackage && !s.runtimeReady() {
		fail(w, 409, "Windows 项目工具运行器缺失")
		return
	}

	s.update.Lock()
	defer s.update.Unlock()
	ctx := r.Context()
	record, err := s.home.identity.localIdentity()
	if err != nil || record.LocalOwner == nil {
		fail(w, 409, "请先准备本机主人身份")
		return
	}
	host, err := s.forFish(record.FishID)
	if err != nil {
		fail(w, 503, "项目工具安装状态不可读")
		return
	}
	current, err := host.store.Installed(ctx, id)
	if in.Action == "install" || in.Action == "upgrade" {
		missing := errors.Is(err, sql.ErrNoRows) || err == nil && current.Removed
		if in.Action == "upgrade" && (err != nil || current.Removed || current.Enabled || current.Revision != in.Revision) {
			fail(w, 409, "请先停用该工具并刷新版本，再确认更新")
			return
		}
		if err != nil && !missing {
			fail(w, 503, "项目工具安装状态不可读")
			return
		}
		contract, contractErr := catalog.ContractFor(id)
		if contractErr != nil {
			fail(w, 503, "官方工具声明不可读")
			return
		}
		if in.Action == "install" && !missing && current.Digest != contract.ManifestSHA256 {
			fail(w, 409, "已安装版本需要单独升级")
			return
		}
		if missing || current.Digest != contract.ManifestSHA256 {
			if s.options.CatalogDirectory == "" {
				fail(w, 409, "当前发行包未提供该项目工具")
				return
			}
			prepared, prepareErr := host.store.Prepare(ctx, filepath.Join(s.options.CatalogDirectory, id))
			if prepareErr != nil || prepared.Package.ID != id || prepared.Digest != contract.ManifestSHA256 {
				fail(w, 409, "项目工具发行源缺失或摘要不匹配")
				return
			}
			grants := []string{"artifact.read", "artifact.write"}
			if id == localRVCPackage {
				grants = localRVCGrants
			}
			if id == localComfyPackage {
				grants = localComfyGrants
			}
			if id == localYuEPackage {
				grants = localYuEGrants
			}
			if id == localTTSPackage {
				grants = localTTSGrants
			}
			_, err = host.store.Install(ctx, prepared.Token, prepared.Digest, grants)
		}
	} else {
		if err != nil || current.Removed || current.Revision != in.Revision {
			fail(w, 409, "项目工具版本已变化，请刷新后重试")
			return
		}
		if id == localTTSPackage {
			if in.Action == "enable" {
				if s.tts == nil {
					fail(w, 409, "本机语音后端未登记")
					return
				}
				if info, checkErr := s.tts.Info(ctx, record.FishID); checkErr != nil || len(info.Voices) == 0 {
					fail(w, 409, localTTSSetupReason(checkErr))
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
					fail(w, 409, "本机 YuE 后端未登记")
					return
				}
				if _, err := s.yue.Info(ctx, record.FishID); err != nil {
					fail(w, 409, "请核对本机 YuE 部署、工作流和 FFmpeg 登记")
					return
				}
			}
			_, err = host.store.SetEnabled(ctx, id, current.Revision, in.Action == "enable")
			if err == nil && in.Action == "disable" && s.yue != nil {
				stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 25*time.Second)
				err = s.yue.Stop(stopCtx)
				cancel()
				if err != nil {
					fail(w, 503, "YuE 已停用，后端退出尚待核对")
					return
				}
			}
		} else if id == localComfyPackage {
			if in.Action == "enable" {
				if _, _, checkErr := s.comfySetup(ctx, record.FishID); checkErr != nil {
					fail(w, 409, checkErr.Error())
					return
				}
			}
			_, err = host.store.SetEnabled(ctx, id, current.Revision, in.Action == "enable")
			if err == nil && in.Action == "disable" && s.comfy != nil {
				stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 25*time.Second)
				err = s.comfy.Stop(stopCtx)
				cancel()
				if err != nil {
					fail(w, 503, "ComfyUI 已停用，新任务已拦截，正在核对后端退出。")
					return
				}
			}
		} else if id == localRVCPackage {
			if in.Action == "enable" {
				if s.rvc == nil {
					fail(w, 409, "当前发行包未提供 RVC 后端")
					return
				}
				info, checkErr := s.rvc.Info(ctx, record.FishID)
				if checkErr != nil || len(info.Profiles) == 0 {
					fail(w, 409, localRVCSetupReason(checkErr))
					return
				}
			}
			_, err = host.store.SetEnabled(ctx, id, current.Revision, in.Action == "enable")
			if err == nil && in.Action == "disable" && s.rvc != nil {
				stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 25*time.Second)
				err = s.rvc.Stop(stopCtx)
				cancel()
				if err != nil {
					fail(w, 503, "RVC 已停用，新任务已拦截，正在核对后端退出。")
					return
				}
			}
		} else if in.Action == "enable" {
			err = host.controller.EnableOnDemand(ctx, id, current.Revision)
		} else {
			err = host.controller.Stop(ctx, id, current.Revision)
		}
	}
	if err != nil {
		fail(w, 409, "项目工具设置未保存，请核对版本、依赖和运行中的任务")
		return
	}
	view, err := s.projectView(ctx, host, id, name)
	if err != nil {
		fail(w, 503, "项目工具状态暂不可读")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	respond(w, 200, map[string]any{"tool": view})
}
