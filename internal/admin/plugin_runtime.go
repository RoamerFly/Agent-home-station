package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/pluginruntime"
)

func (m *Manager) installedPluginRuntime() (*pluginruntime.Controller, bool, error) {
	store, err := m.packageStore()
	if err != nil {
		return nil, false, err
	}
	m.packageMu.Lock()
	defer m.packageMu.Unlock()
	if m.packagesClosed {
		return nil, false, errors.New("plugin host closing")
	}
	if m.packageRuntime != nil {
		return m.packageRuntime, m.packageRuntimeReady, nil
	}
	var options pluginruntime.Options
	raw := os.Getenv("WECHAT_PLUGIN_RUNTIME_CONFIG")
	if raw != "" {
		d := json.NewDecoder(bytes.NewBufferString(raw))
		d.DisallowUnknownFields()
		if d.Decode(&options) != nil {
			return nil, false, errors.New("plugin runtime deployment configuration invalid")
		}
	}
	ready := false
	if runtime.GOOS == "windows" {
		ready = options.Pythonw != "" && options.WindowsHelper != ""
		for _, p := range []string{options.Pythonw, options.WindowsHelper} {
			if st, e := os.Stat(p); e != nil || !st.Mode().IsRegular() {
				ready = false
			}
		}
	} else if runtime.GOOS == "linux" {
		_, e := exec.LookPath("bwrap")
		ready = e == nil
	}
	var executor pluginhost.Executor
	if ready {
		executor = pluginruntime.InstalledExecutor(store, options)
	}
	h, err := pluginruntime.NewController(store, executor)
	if err != nil {
		return nil, false, err
	}
	m.packageRuntime = h
	m.packageRuntimeReady = ready
	return h, ready, nil
}

func (s *Server) pluginRuntimeRoute(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/plugin-packages/{id}/runtime", s.protected(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Action         string `json:"action"`
			Revision       int    `json:"revision"`
			TimeoutSeconds *int   `json:"timeout_seconds"`
		}
		if !decode(w, r, &input) {
			return
		}
		if input.Action != "start" && input.Action != "stop" {
			fail(w, 400, "请选择启动或停止")
			return
		}
		seconds := 30
		if input.TimeoutSeconds != nil {
			seconds = *input.TimeoutSeconds
		}
		if seconds < 0 || int64(seconds) > int64((time.Duration(1<<63-1))/time.Second) {
			fail(w, 400, "启动等待时间无效")
			return
		}
		if s.manager.draining() {
			fail(w, 409, "服务正在收尾，请稍后重试")
			return
		}
		h, _, err := s.manager.installedPluginRuntime()
		if err != nil {
			fail(w, 503, "插件执行环境未就绪")
			return
		}
		ctx := r.Context()
		if seconds > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
			defer cancel()
		}
		id := r.PathValue("id")
		if input.Action == "start" {
			err = h.Start(ctx, id, input.Revision)
		} else {
			err = h.Stop(ctx, id, input.Revision)
		}
		if err != nil {
			fail(w, 409, "操作未完成：请检查配置、授权、依赖或运行环境；有在途调用时需先等待结束。刷新后可核对当前状态。")
			return
		}
		store, err := s.manager.packageStore()
		if err != nil {
			fail(w, 503, "插件宿主正在关闭")
			return
		}
		i, err := store.Installed(r.Context(), id)
		if err != nil {
			fail(w, 409, "插件状态已变化，请刷新")
			return
		}
		respond(w, 200, map[string]any{"installed": i, "runtime": h.Status(id)})
	}))
}
