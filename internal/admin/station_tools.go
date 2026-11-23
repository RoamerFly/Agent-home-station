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

	"wechat-bot/internal/pluginhost"
)

// stationToolView is deliberately a projection of the Station public tool
// view. It keeps paths, tokens and implementation details out of the Agent
// conversation while allowing future tools to appear without a Go enum.
type stationToolView struct {
	DirectoryPackage bool                         `json:"directory_package"`
	Backends         []pluginhost.ToolBackendView `json:"backends,omitempty"`
	ExternalPackage  bool                         `json:"external_package,omitempty"`
	ID               string                       `json:"id"`
	Name             string                       `json:"name"`
	Installed        bool                         `json:"installed"`
	Enabled          bool                         `json:"enabled"`
	Revision         int                          `json:"revision"`
	State            string                       `json:"state"`
	RuntimeReady     bool                         `json:"runtime_ready"`
	Reason           string                       `json:"reason,omitempty"`
	VoiceProfiles    any                          `json:"voice_profiles,omitempty"`
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
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/session", nil)
	if err != nil {
		return stationToolSession{}, err
	}
	req.Header.Set("Origin", base)
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
	timeout := 90 * time.Second
	limit := int64(8 << 20)
	if path == stationSpeechRoute && method == http.MethodPost {
		timeout = 5 * time.Minute
		limit = 45 << 20
	}
	if path == "/api/local/chat" && method == http.MethodPost {
		timeout = 3 * time.Minute
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return nil, errors.New("station_tool_request_unavailable")
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
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

// The legacy WeChat worker remains the sole owner of its native session. This
// compatibility call gives it the same registered workstation generator as
// local chat without transferring group delivery or duplicating a worker.
func (p *permittedTools) callStationComfy(ctx context.Context, raw json.RawMessage) (string, error) {
	if !p.privileged() || !p.workspaceExecutionAllowed() || p.trace.TraceID == "" {
		return "", toolInputError("station_comfy_not_authorized")
	}
	var in struct {
		Action     string                     `json:"action"`
		WorkflowID string                     `json:"workflow_id"`
		Inputs     map[string]json.RawMessage `json:"inputs"`
		JobID      string                     `json:"job_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil {
		return "", toolInputError("station_comfy_arguments_invalid")
	}
	var instruction string
	switch in.Action {
	case "workflows":
		instruction = "只调用本机 project_comfy_workflows，逐项返回工作流编号、用途和参数，不发送到微信。"
	case "run":
		if in.WorkflowID == "" || len(in.WorkflowID) > 96 || !validID(in.WorkflowID) {
			return "", toolInputError("station_comfy_workflow_required")
		}
		parameters, _ := json.Marshal(in.Inputs)
		instruction = "调用本机 project_comfy_run，workflow_id=" + in.WorkflowID + "，inputs=" + string(parameters) + "。返回实际 job_id 和状态；若已完成也返回 artifact_id。不发送到微信。"
	case "job":
		if in.JobID == "" || len(in.JobID) > 128 || !validID(in.JobID) {
			return "", toolInputError("station_comfy_job_required")
		}
		instruction = "只调用本机 project_comfy_job 查询 job_id=" + in.JobID + " 的 status，返回实际状态和已登记的 artifact_id。不发送到微信。"
	default:
		return "", toolInputError("station_comfy_action_invalid")
	}
	session, err := p.stationToolLogin(ctx)
	if err != nil {
		return "", err
	}
	requestID := "wechat-comfy-" + agentID(p.trace.Account, p.trace.Thread, p.trace.TraceID, string(raw))
	data, err := p.stationToolRequest(ctx, session, http.MethodPost, "/api/local/chat", map[string]string{"request_id": requestID, "text": instruction})
	if err != nil {
		return "", err
	}
	var result struct {
		Messages []string `json:"messages"`
		Status   string   `json:"status"`
	}
	if json.Unmarshal(data, &result) != nil || len(result.Messages) == 0 {
		return "", errors.New("station_comfy_result_unavailable")
	}
	return strings.Join(result.Messages, "\n"), nil
}

func (p *permittedTools) callStationTool(ctx context.Context, action string, raw json.RawMessage) (string, error) {
	if action != "status" && action != "enable" && action != "start" && action != "stop" && action != "restart" {
		return "", toolInputError("station_tool_action_invalid")
	}
	if !p.privileged() && (action == "enable" || !p.workspaceExecutionAllowed()) {
		return "", toolInputError("station_tool_not_authorized")
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
			out, _ := json.Marshal(view)
			return string(out), nil
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
	if action == "enable" && (!current.ExternalPackage || !current.Installed) {
		return "", errors.New("station_tool_owner_install_required")
	}
	if action == "start" || action == "restart" {
		if !current.Enabled {
			return "", errors.New("station_tool_disabled")
		}
	}
	path := "/api/local/project-tools/" + url.PathEscape(id)
	response, err := p.stationToolRequest(ctx, session, http.MethodPut, path, map[string]any{"action": action, "revision": current.Revision, "idle_only": !p.privileged()})
	if err != nil {
		return "", err
	}
	return string(response), nil
}
