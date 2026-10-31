package admin

import (
	"errors"
	"net/http"
	"path/filepath"
	"strconv"

	"wechat-bot/internal/pluginhost"
	catalog "wechat-bot/plugins"
)

// The new package store is opened lazily. Reading the old plugin settings or
// starting the existing application does not migrate or change its configuration.
func (m *Manager) packageStore() (*pluginhost.Store, error) {
	m.packageMu.Lock()
	defer m.packageMu.Unlock()
	if m.packagesClosed {
		return nil, errors.New("插件宿主正在关闭")
	}
	if m.packages != nil {
		return m.packages, nil
	}
	store, err := pluginhost.OpenStore(filepath.Join(m.dir, "plugin-host"), m.native.Account())
	if err != nil {
		return nil, err
	}
	m.packages = store
	return store, nil
}
func (s *Server) pluginPackageRoutes(mux *http.ServeMux) {
	s.pluginRuntimeRoute(mux)
	mux.HandleFunc("GET /api/plugin-packages/{id}", s.protected(func(w http.ResponseWriter, r *http.Request) {
		store, err := s.manager.packageStore()
		if err != nil {
			fail(w, 500, "插件存储不可用")
			return
		}
		id := r.PathValue("id")
		installed, err := store.Installed(r.Context(), id)
		if err != nil {
			fail(w, 404, "插件尚未安装")
			return
		}
		schema, err := store.ConfigSchema(r.Context(), id)
		if err != nil {
			fail(w, 409, "插件配置声明无效或已变化")
			return
		}
		config, err := store.Configuration(r.Context(), id)
		if err != nil {
			fail(w, 500, "插件配置读取失败")
			return
		}
		h, connected, runtimeErr := s.manager.installedPluginRuntime()
		var status any
		if runtimeErr == nil {
			status = h.Status(id)
		}
		respond(w, 200, map[string]any{"installed": installed, "schema": schema, "configuration": config, "runtime_connected": connected, "runtime": status})
	}))
	mux.HandleFunc("POST /api/plugin-packages/{id}/configuration", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var input pluginhost.ConfigUpdate
		if !decode(w, r, &input) {
			return
		}
		if s.manager.draining() {
			fail(w, 409, "服务正在收尾，请稍后重试")
			return
		}
		store, err := s.manager.packageStore()
		if err != nil {
			fail(w, 500, "插件存储不可用")
			return
		}
		value, err := store.SaveConfiguration(r.Context(), r.PathValue("id"), input)
		if err != nil {
			fail(w, 409, "配置未保存：请检查字段、保存版本，或先停用插件")
			return
		}
		respond(w, 200, value)
	}))
	mux.HandleFunc("GET /api/plugin-packages/{id}/jobs", s.protected(func(w http.ResponseWriter, r *http.Request) {
		limit := 30
		after := int64(0)
		var err error
		if raw, ok := r.URL.Query()["limit"]; ok {
			limit, err = strconv.Atoi(raw[0])
			if err != nil || limit < 1 {
				fail(w, 400, "页大小必须为正整数")
				return
			}
		}
		if raw, ok := r.URL.Query()["after"]; ok {
			after, err = strconv.ParseInt(raw[0], 10, 64)
			if err != nil || after < 0 {
				fail(w, 400, "续读位置无效")
				return
			}
		}
		store, err := s.manager.packageStore()
		if err != nil {
			fail(w, 500, "插件存储不可用")
			return
		}
		page, err := store.Jobs(r.Context(), r.PathValue("id"), after, limit)
		if err != nil {
			fail(w, 500, "插件任务读取失败")
			return
		}
		respond(w, 200, page)
	}))
	mux.HandleFunc("POST /api/plugin-packages/{id}/jobs/{job}/cancel", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Revision int `json:"revision"`
		}
		if !decode(w, r, &input) {
			return
		}
		store, err := s.manager.packageStore()
		if err != nil {
			fail(w, 500, "插件存储不可用")
			return
		}
		job, err := store.Job(r.Context(), r.PathValue("job"))
		if err != nil || job.Plugin != r.PathValue("id") {
			fail(w, 404, "插件任务不存在")
			return
		}
		job, err = store.RequestJobCancel(r.Context(), job.ID, input.Revision)
		if err != nil {
			fail(w, 409, "任务状态已变化，请刷新后重试")
			return
		}
		respond(w, 200, job)
	}))
	mux.HandleFunc("GET /api/plugin-packages", s.protected(func(w http.ResponseWriter, r *http.Request) {
		store, err := s.manager.packageStore()
		if err != nil {
			fail(w, 500, "插件存储不可用")
			return
		}
		installed, err := store.List(r.Context())
		if err != nil {
			fail(w, 500, "插件登记读取失败")
			return
		}
		official, err := catalog.List()
		if err != nil {
			fail(w, 500, "插件目录读取失败")
			return
		}
		h, connected, runtimeErr := s.manager.installedPluginRuntime()
		statuses := map[string]any{}
		if runtimeErr == nil {
			for _, item := range installed {
				statuses[item.Package.ID] = h.Status(item.Package.ID)
			}
		}
		respond(w, 200, map[string]any{"installed": installed, "catalog": official, "runtime_connected": connected, "runtimes": statuses})
	}))
	mux.HandleFunc("POST /api/plugin-packages/prepare", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Directory string `json:"directory"`
		}
		if !decode(w, r, &input) {
			return
		}
		if input.Directory == "" || !filepath.IsAbs(input.Directory) {
			fail(w, 400, "请选择本机插件包的绝对目录")
			return
		}
		if s.manager.draining() {
			fail(w, 409, "服务正在收尾，请稍后重试")
			return
		}
		store, err := s.manager.packageStore()
		if err != nil {
			fail(w, 500, "插件存储不可用")
			return
		}
		prepared, err := store.Prepare(r.Context(), input.Directory)
		if err != nil {
			fail(w, 400, "插件包校验失败；请检查清单、文件摘要和目录权限")
			return
		}
		respond(w, 200, prepared)
	}))
	mux.HandleFunc("POST /api/plugin-packages/import", s.protected(func(w http.ResponseWriter, r *http.Request) {
		if s.manager.draining() {
			fail(w, 409, "服务正在收尾，请稍后重试")
			return
		}
		if r.Header.Get("Content-Type") != "application/zip" {
			fail(w, 415, "请选择 ZIP 格式的插件包")
			return
		}
		if r.ContentLength > pluginhost.MaxArchiveBytes {
			fail(w, 413, "插件 ZIP 超过 64 MiB；运行环境和模型请作为独立资源安装")
			return
		}
		store, err := s.manager.packageStore()
		if err != nil {
			fail(w, 500, "插件存储不可用")
			return
		}
		prepared, err := store.PrepareArchive(r.Context(), r.Body)
		if errors.Is(err, pluginhost.ErrArchiveLimit) {
			fail(w, 413, "插件包超过导入限制：ZIP 64 MiB、展开后 256 MiB、4096 个条目；模型和运行环境应独立安装")
			return
		}
		if err != nil {
			fail(w, 400, "插件包检查失败：需要完整的 ZIP 插件包、有效清单和文件摘要，不能包含链接或重复路径")
			return
		}
		respond(w, 200, prepared)
	}))
	mux.HandleFunc("POST /api/plugin-packages/install", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Token       string   `json:"token"`
			Digest      string   `json:"digest"`
			Permissions []string `json:"permissions"`
			Confirm     bool     `json:"confirm"`
		}
		if !decode(w, r, &input) {
			return
		}
		if !input.Confirm {
			fail(w, 400, "需要管理员确认安装和所选权限")
			return
		}
		if s.manager.draining() {
			fail(w, 409, "服务正在收尾，请稍后重试")
			return
		}
		store, err := s.manager.packageStore()
		if err != nil {
			fail(w, 500, "插件存储不可用")
			return
		}
		installed, err := store.Install(r.Context(), input.Token, input.Digest, input.Permissions)
		if err != nil {
			fail(w, 409, "安装未完成：包尚未就绪、确认已失效、权限不匹配或文件已变化，请重新检查")
			return
		}
		respond(w, 201, installed)
	}))
}
