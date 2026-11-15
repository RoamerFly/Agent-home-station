package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// stationToolView is deliberately a projection of the Station public tool
// view. It keeps paths, tokens and implementation details out of the Agent
// conversation while allowing future tools to appear without a Go enum.
type stationToolView struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Installed     bool   `json:"installed"`
	Enabled       bool   `json:"enabled"`
	Revision      int    `json:"revision"`
	State         string `json:"state"`
	RuntimeReady  bool   `json:"runtime_ready"`
	Reason        string `json:"reason,omitempty"`
	VoiceProfiles any    `json:"voice_profiles,omitempty"`
}

type stationToolSession struct {
	BaseURL string
	Cookie  string
	CSRF    string
}

func stationToolIDs(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if id == "" || len(id) > 96 || !validID(id) {
		return "", toolInputError("station_tool_id_invalid")
	}
	return id, nil
}

func (p *permittedTools) stationToolBaseURL() string {
	base := strings.TrimSpace(os.Getenv("AGENT_STATION_URL"))
	if base == "" {
		base = "http://127.0.0.1:8082"
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	return strings.TrimRight(base, "/")
}

func (p *permittedTools) stationToolPasswordPath() string {
	if path := strings.TrimSpace(os.Getenv("AGENT_STATION_ADMIN_PASSWORD_FILE")); path != "" {
		return path
	}
	// Portable release layout: data/applications/wechat/legacy-admin ->
	// data/main_setting/authority/admin-password.
	return filepath.Clean(filepath.Join(p.manager.dir, "..", "..", "..", "main_setting", "authority", "admin-password"))
}

func (p *permittedTools) stationToolLogin(ctx context.Context) (stationToolSession, error) {
	base := p.stationToolBaseURL()
	if base == "" {
		return stationToolSession{}, errors.New("station_tool_endpoint_invalid")
	}
	password, err := os.ReadFile(p.stationToolPasswordPath())
	if err != nil || strings.TrimSpace(string(password)) == "" {
		return stationToolSession{}, errors.New("station_tool_authority_unavailable")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	body, _ := json.Marshal(map[string]string{"password": strings.TrimSpace(string(password))})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/login", bytes.NewReader(body))
	if err != nil {
		return stationToolSession{}, err
	}
	req.Header.Set("Origin", base)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return stationToolSession{}, errors.New("station_tool_login_unavailable")
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return stationToolSession{}, errors.New("station_tool_login_rejected")
	}
	var login struct {
		Authenticated bool   `json:"authenticated"`
		CSRF          string `json:"csrf"`
	}
	if json.Unmarshal(data, &login) != nil || !login.Authenticated || login.CSRF == "" {
		return stationToolSession{}, errors.New("station_tool_login_invalid")
	}
	cookie := ""
	for _, c := range resp.Cookies() {
		if c.Name != "" && c.Value != "" {
			cookie = c.Name + "=" + c.Value
			break
		}
	}
	if cookie == "" {
		return stationToolSession{}, errors.New("station_tool_session_missing")
	}
	return stationToolSession{BaseURL: base, Cookie: cookie, CSRF: login.CSRF}, nil
}

func (p *permittedTools) stationToolRequest(ctx context.Context, session stationToolSession, method, path string, body any) ([]byte, error) {
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, session.BaseURL+path, payload)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Origin", session.BaseURL)
	req.Header.Set("Cookie", session.Cookie)
	if method != http.MethodGet {
		req.Header.Set("X-CSRF-Token", session.CSRF)
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return nil, errors.New("station_tool_request_unavailable")
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &failure) == nil && failure.Error != "" {
			return nil, errors.New(failure.Error)
		}
		return nil, fmt.Errorf("station_tool_request_failed_%d", resp.StatusCode)
	}
	return data, nil
}

func (p *permittedTools) callStationTool(ctx context.Context, action string, raw json.RawMessage) (string, error) {
	if !p.privileged() {
		return "", toolInputError("station_tool_requires_owner")
	}
	var in struct {
		ToolID string `json:"tool_id"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", toolInputError("station_tool_arguments_invalid")
	}
	if action != "status" {
		if _, err := stationToolIDs(in.ToolID); err != nil {
			return "", err
		}
	}
	session, err := p.stationToolLogin(ctx)
	if err != nil {
		return "", err
	}
	if action == "status" {
		data, err := p.stationToolRequest(ctx, session, http.MethodGet, "/api/local/project-tools", nil)
		if err != nil {
			return "", err
		}
		var view struct {
			Tools []stationToolView `json:"tools"`
		}
		if json.Unmarshal(data, &view) != nil {
			return "", errors.New("station_tool_status_invalid")
		}
		if in.ToolID == "" {
			return string(data), nil
		}
		for _, tool := range view.Tools {
			if tool.ID == in.ToolID {
				out, _ := json.Marshal(tool)
				return string(out), nil
			}
		}
		return "", errors.New("station_tool_not_found")
	}
	id, _ := stationToolIDs(in.ToolID)
	data, err := p.stationToolRequest(ctx, session, http.MethodGet, "/api/local/project-tools", nil)
	if err != nil {
		return "", err
	}
	var view struct {
		Tools []stationToolView `json:"tools"`
	}
	if json.Unmarshal(data, &view) != nil {
		return "", errors.New("station_tool_status_invalid")
	}
	var current *stationToolView
	for i := range view.Tools {
		if view.Tools[i].ID == id {
			current = &view.Tools[i]
			break
		}
	}
	if current == nil {
		return "", errors.New("station_tool_not_found")
	}
	if action == "start" || action == "restart" {
		if !current.Enabled {
			return "", errors.New("station_tool_disabled")
		}
	}
	path := "/api/local/project-tools/" + url.PathEscape(id)
	response, err := p.stationToolRequest(ctx, session, http.MethodPut, path, map[string]any{"action": action, "revision": current.Revision})
	if err != nil {
		return "", err
	}
	return string(response), nil
}
