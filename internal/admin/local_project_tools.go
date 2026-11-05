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
	"sync/atomic"
	"time"

	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
	"wechat-bot/internal/workspacefs"
	catalog "wechat-bot/plugins"
)

const localProjectToolIdle = 5 * time.Minute

type localProjectTools struct {
	home               *localHomeService
	options            pluginruntime.Options
	mu                 sync.Mutex
	update             sync.Mutex
	host               *localProjectToolHost
	closed             bool
	documentReadActive atomic.Bool
}

type localProjectToolHost struct {
	store      *pluginhost.Store
	controller *pluginruntime.Controller
}

type localProjectToolView struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	CatalogAvailable bool   `json:"catalog_available"`
	RuntimeReady     bool   `json:"runtime_ready"`
	PDFReady         bool   `json:"pdf_ready"`
	UpdateAvailable  bool   `json:"update_available"`
	Installed        bool   `json:"installed"`
	Enabled          bool   `json:"enabled"`
	Revision         int    `json:"revision"`
	State            string `json:"state"`
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
	if s.host == nil {
		return nil
	}
	err := errors.Join(s.host.controller.Close(), s.host.store.Close())
	s.host = nil
	return err
}

func (s *localProjectTools) documentView(ctx context.Context, host *localProjectToolHost) (localProjectToolView, error) {
	view := localProjectToolView{ID: documentWorkshopPluginID, Name: "文档工坊", RuntimeReady: s.runtimeReady(), State: "not_installed"}
	if s.options.CatalogDirectory != "" {
		info, err := os.Lstat(filepath.Join(s.options.CatalogDirectory, view.ID, "plugin.json"))
		view.CatalogAvailable = err == nil && info.Mode().IsRegular()
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
	contract, err := catalog.ContractFor(documentWorkshopPluginID)
	if err != nil {
		return view, err
	}
	view.UpdateAvailable = view.CatalogAvailable && item.Digest != contract.ManifestSHA256
	view.PDFReady = item.Digest == contract.ManifestSHA256 && s.pdfRuntimeReady()
	switch {
	case !item.Enabled:
		view.State = "disabled"
	case !view.RuntimeReady:
		view.State = "runtime_missing"
	case s.documentReadActive.Load():
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
		view, err := s.documentView(r.Context(), host)
		if err != nil {
			fail(w, 503, "项目工具安装状态不可读")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		respond(w, 200, map[string]any{"tools": []localProjectToolView{view}})
	}))
	mux.HandleFunc("PUT /api/local/project-tools/document-workshop", owner.protected(s.changeDocumentTool))
}

func (s *localProjectTools) changeDocumentTool(w http.ResponseWriter, r *http.Request) {
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
		fail(w, 400, "安装需要主人明确授予本机成品读写权限")
		return
	}
	if in.Action == "enable" && !s.runtimeReady() {
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
	const id = documentWorkshopPluginID
	current, err := host.store.Installed(ctx, id)
	if in.Action == "install" || in.Action == "upgrade" {
		missing := errors.Is(err, sql.ErrNoRows) || err == nil && current.Removed
		if in.Action == "upgrade" && (err != nil || current.Removed || current.Enabled || current.Revision != in.Revision) {
			fail(w, 409, "请先停用文档工坊并刷新版本，再确认更新")
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
				fail(w, 409, "当前发行包未提供文档工坊")
				return
			}
			prepared, prepareErr := host.store.Prepare(ctx, filepath.Join(s.options.CatalogDirectory, id))
			if prepareErr != nil || prepared.Package.ID != id || prepared.Digest != contract.ManifestSHA256 {
				fail(w, 409, "文档工坊发行源缺失或摘要不匹配")
				return
			}
			_, err = host.store.Install(ctx, prepared.Token, prepared.Digest, []string{"artifact.read", "artifact.write"})
		}
	} else {
		if err != nil || current.Removed || current.Revision != in.Revision {
			fail(w, 409, "项目工具版本已变化，请刷新后重试")
			return
		}
		if in.Action == "enable" {
			err = host.controller.EnableOnDemand(ctx, id, current.Revision)
		} else {
			err = host.controller.Stop(ctx, id, current.Revision)
		}
	}
	if err != nil {
		fail(w, 409, "项目工具设置未保存，请核对版本、依赖和运行中的任务")
		return
	}
	view, err := s.documentView(ctx, host)
	if err != nil {
		fail(w, 503, "项目工具状态暂不可读")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	respond(w, 200, map[string]any{"tool": view})
}
