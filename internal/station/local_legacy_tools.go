package station

// Read-only projection of existing WeChat tool configurations for the
// application tools view. Public tool lifecycle never calls this host.
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
	"sync"
	"time"
	"wechat-bot/internal/pluginhost"
	catalog "wechat-bot/plugins"
)

type legacyToolClient struct {
	client   *http.Client
	csrf     string
	base     string
	password func() ([]byte, error)
	mu       sync.Mutex
}

const legacyToolBase = "http://127.0.0.1:8080"

func (s *localProjectTools) legacyToolClient(ctx context.Context) (*legacyToolClient, error) {
	s.legacyClientMu.Lock()
	defer s.legacyClientMu.Unlock()
	if s.legacyClient != nil {
		return s.legacyClient, nil
	}
	dir := filepath.Join(s.home.dataDir, "applications", "wechat", "legacy-admin")
	jar, _ := cookiejar.New(nil)
	c := &legacyToolClient{base: legacyToolBase, client: &http.Client{Jar: jar, Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	c.password = func() ([]byte, error) {
		for _, name := range []string{"admin-password", "admin-token"} {
			b, e := os.ReadFile(filepath.Join(dir, name))
			if e == nil && len(b) > 0 && len(b) <= 1024 {
				return b, nil
			}
		}
		return nil, errors.New("旧工具宿主未接入")
	}
	if err := c.login(ctx); err != nil {
		return nil, err
	}
	s.legacyClient = c
	return c, nil
}
func (c *legacyToolClient) login(ctx context.Context) error {
	password, err := c.password()
	if err != nil {
		return err
	}
	raw, _, err := c.send(ctx, http.MethodPost, "/api/login", map[string]string{"password": strings.TrimSpace(string(password))})
	if err != nil {
		return err
	}
	var login struct {
		Authenticated bool   `json:"authenticated"`
		CSRF          string `json:"csrf"`
	}
	if json.Unmarshal(raw, &login) != nil || !login.Authenticated || login.CSRF == "" {
		return errors.New("旧工具宿主认证不可用")
	}
	c.csrf = login.CSRF
	return nil
}
func (c *legacyToolClient) request(ctx context.Context, method, path string, value any) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, status, err := c.send(ctx, method, path, value)
	// An authentication rejection happens before execution. Never replay timeout,
	// transport failure or a server error: a mutation may already be committed.
	if status == http.StatusUnauthorized {
		if e := c.login(ctx); e != nil {
			return nil, e
		}
		raw, _, err = c.send(ctx, method, path, value)
	}
	return raw, err
}
func (c *legacyToolClient) send(ctx context.Context, method, path string, value any) ([]byte, int, error) {
	var body []byte
	if value != nil {
		body, _ = json.Marshal(value)
	}
	r, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	r.Header.Set("Origin", c.base)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", c.csrf)
	response, err := c.client.Do(r)
	if err != nil {
		return nil, 0, errors.New("旧工具宿主离线")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, response.StatusCode, err
	}
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &failure)
		if failure.Error != "" {
			return nil, response.StatusCode, errors.New(failure.Error)
		}
		return nil, response.StatusCode, errors.New("旧工具宿主请求失败")
	}
	return raw, response.StatusCode, nil
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
		Installed []pluginhost.InstalledPackage           `json:"installed"`
		Connected bool                                    `json:"runtime_connected"`
		Backends  map[string][]pluginhost.ToolBackendView `json:"backends"`
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
	publicTools := s.publicToolIDs()
	for _, p := range data.Installed {
		if p.Removed || p.Package.ID == "mobile-control" {
			continue
		}
		// Public tools are owned by the Station catalog and are already exposed
		// by /api/local/project-tools.  The legacy admin registry may still hold
		// a historical copy of their configuration, but presenting it here makes
		// the application tab look as if the tool is WeChat-only and gives it a
		// second, stale lifecycle state.  Keep this projection for genuinely
		// application-scoped packages only.
		if _, ok := publicTools[p.Package.ID]; ok {
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
		v.Backends = data.Backends[v.ID]
		for i := range v.Backends {
			v.Backends[i].Scope = "微信工具宿主"
		}
		views[v.ID] = v
	}
	return views
}

// publicToolIDs returns the IDs whose lifecycle belongs to the Station public
// tool catalog.  The legacy admin store is retained for compatibility with
// old WeChat application packages, but it must not re-advertise a public
// package in the application's private-tools tab.  Include the embedded
// catalog as well as directory discovery so this remains correct when a new
// public package is added without changing this adapter.
func (s *localProjectTools) publicToolIDs() map[string]struct{} {
	ids := map[string]struct{}{"music": {}}
	for _, p := range localProjectPackages {
		ids[p.ID] = struct{}{}
	}
	if items, err := catalog.List(); err == nil {
		for _, p := range items {
			if p.ID != "mobile-control" {
				ids[p.ID] = struct{}{}
			}
		}
	}
	for id := range discoverToolPackages(s.options.CatalogDirectory) {
		ids[id] = struct{}{}
	}
	return ids
}
