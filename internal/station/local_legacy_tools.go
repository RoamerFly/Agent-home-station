package station

// Transitional discovery uses the existing tool host in place: no duplicated
// package stores, credentials or private configurations. App-specific tools
// remain in their application; only installed public packages are projected.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"time"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/websession"
)

type legacyToolClient struct {
	client *http.Client
	csrf   string
}

const legacyToolBase = "http://127.0.0.1:8080"

func (s *localProjectTools) legacyToolClient(ctx context.Context) (*legacyToolClient, error) {
	dir := filepath.Join(s.home.dataDir, "applications", "wechat", "legacy-admin")
	var password []byte
	for _, name := range []string{"admin-password", "admin-token"} {
		b, e := os.ReadFile(filepath.Join(dir, name))
		if e == nil {
			password = b
			break
		}
	}
	if len(password) == 0 || len(password) > 1024 {
		return nil, errors.New("旧工具宿主未接入")
	}
	jar, _ := cookiejar.New(nil)
	c := &legacyToolClient{client: &http.Client{Jar: jar, Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	raw, err := c.request(ctx, "POST", "/api/login", map[string]string{"password": strings.TrimSpace(string(password))})
	if err != nil {
		return nil, err
	}
	var login struct {
		Authenticated bool   `json:"authenticated"`
		CSRF          string `json:"csrf"`
	}
	if json.Unmarshal(raw, &login) != nil || !login.Authenticated || login.CSRF == "" {
		return nil, errors.New("旧工具宿主认证不可用")
	}
	c.csrf = login.CSRF
	return c, nil
}
func (c *legacyToolClient) request(ctx context.Context, method, path string, value any) ([]byte, error) {
	var body []byte
	if value != nil {
		body, _ = json.Marshal(value)
	}
	r, err := http.NewRequestWithContext(ctx, method, legacyToolBase+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Origin", legacyToolBase)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", c.csrf)
	response, err := c.client.Do(r)
	if err != nil {
		return nil, errors.New("旧工具宿主离线")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != 200 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &failure)
		if failure.Error != "" {
			return nil, errors.New(failure.Error)
		}
		return nil, errors.New("旧工具宿主请求失败")
	}
	return raw, nil
}
func (s *localProjectTools) legacyToolViews(ctx context.Context) map[string]localProjectToolView {
	s.legacyMu.Lock()
	defer s.legacyMu.Unlock()
	if time.Since(s.legacyAt) < 3*time.Second {
		return s.legacyViews
	}
	views := map[string]localProjectToolView{}
	s.legacyAt = time.Now()
	s.legacyViews = views
	statusCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	ctx = statusCtx
	c, err := s.legacyToolClient(ctx)
	if err != nil {
		return views
	}
	raw, err := c.request(ctx, "GET", "/api/plugin-packages", nil)
	if err != nil {
		return views
	}
	var data struct {
		Installed []pluginhost.InstalledPackage `json:"installed"`
		Connected bool                          `json:"runtime_connected"`
		Runtimes  map[string]struct {
			State string `json:"state"`
		} `json:"runtimes"`
		Readiness map[string]struct {
			State   string `json:"state"`
			Reasons []struct {
				Message string `json:"message"`
			} `json:"reasons"`
		} `json:"readiness"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return views
	}
	for _, p := range data.Installed {
		if p.Removed || p.Package.ID == "mobile-control" {
			continue
		}
		v := localProjectToolView{ID: p.Package.ID, Name: p.Package.Name, Installed: true, Enabled: p.Enabled, Revision: p.Revision, RuntimeReady: data.Connected, State: data.Runtimes[p.Package.ID].State, Host: "legacy"}
		if !p.Enabled {
			v.State = "disabled"
		} else if v.State == "stopped" || v.State == "" {
			v.State = "idle"
		}
		ready := data.Readiness[p.Package.ID]
		if ready.State == "not_enableable" || ready.State == "runtime_unavailable" || ready.State == "unknown" {
			v.RuntimeReady = false
		}
		for _, r := range ready.Reasons {
			if v.Reason != "" {
				v.Reason += "；"
			}
			v.Reason += r.Message
		}
		views[v.ID] = v
	}
	return views
}
func (s *localProjectTools) changeLegacyTool(w http.ResponseWriter, r *http.Request, view localProjectToolView) {
	var in struct {
		Action   string `json:"action"`
		Revision int    `json:"revision"`
		Approve  bool   `json:"approve_artifacts"`
	}
	if !websession.Decode(w, r, &in) {
		return
	}
	if in.Revision != view.Revision {
		websession.Fail(w, 409, "工具状态已变化，请刷新")
		return
	}
	if in.Action != "enable" && in.Action != "disable" && in.Action != "start" && in.Action != "stop" && in.Action != "restart" {
		websession.Fail(w, 400, "请在原工具配置中管理安装版本")
		return
	}
	if (in.Action == "start" || in.Action == "restart") && !view.Enabled {
		websession.Fail(w, 409, "请先启用工具")
		return
	}
	c, err := s.legacyToolClient(r.Context())
	// Service control itself runs through the music package. Stop its backend
	// before closing that package's executor, so stop/disable can complete.
	if err == nil && view.ID == "music" && (in.Action == "stop" || in.Action == "disable" || in.Action == "restart") {
		_, err = c.request(r.Context(), "POST", "/api/speech/music-service/stop", map[string]any{})
	}
	if err == nil {
		action := in.Action
		if action == "enable" {
			action = "enable_on_demand"
		}
		if action == "disable" {
			action = "stop"
		}
		if action == "stop" && in.Action == "stop" {
			action = "stop_session"
		}
		_, err = c.request(r.Context(), "POST", "/api/plugin-packages/"+view.ID+"/runtime", map[string]any{"action": action, "revision": view.Revision})
	}
	if err == nil && view.ID == "music" {
		switch in.Action {
		case "start":
			_, err = c.request(r.Context(), "POST", "/api/speech/music-service/start", map[string]any{})
		case "restart":
			_, err = c.request(r.Context(), "POST", "/api/speech/music-service/start", map[string]any{})
		}
	}
	if err != nil {
		websession.Fail(w, 409, err.Error())
		return
	}
	s.legacyMu.Lock()
	s.legacyAt = time.Time{}
	s.legacyMu.Unlock()
	v, ok := s.legacyToolViews(r.Context())[view.ID]
	if !ok {
		websession.Fail(w, 503, "操作已提交，状态暂不可读")
		return
	}
	websession.Respond(w, 200, map[string]any{"tool": v})
}
