package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/plugins"
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

func compactStationToolStatus(views []stationToolView) []map[string]any {
	tools := make([]map[string]any, 0, len(views))
	for _, tool := range views {
		tools = append(tools, map[string]any{
			"id": tool.ID, "name": tool.Name, "installed": tool.Installed,
			"enabled": tool.Enabled, "state": tool.State,
			"runtime_ready": tool.RuntimeReady, "reason": tool.Reason,
		})
	}
	return tools
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
	if !p.workspaceExecutionAllowed() || p.trace.TraceID == "" {
		return "", toolInputError("station_comfy_not_authorized")
	}
	var in struct {
		Action          string                     `json:"action"`
		WorkflowID      string                     `json:"workflow_id"`
		Inputs          map[string]json.RawMessage `json:"inputs"`
		JobID           string                     `json:"job_id"`
		ArtifactID      string                     `json:"artifact_id"`
		Category        string                     `json:"category"`
		Offset          int                        `json:"offset"`
		Limit           int                        `json:"limit"`
		Goal            string                     `json:"goal"`
		SourceMessageID string                     `json:"source_message_id"`
		SourcePath      string                     `json:"source_path"`
		FishHome        bool                       `json:"fish_home"`
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
	case "models":
		if in.Offset < 0 || in.Limit < 0 || len(in.Category) > 32 {
			return "", toolInputError("station_comfy_arguments_invalid")
		}
		instruction = fmt.Sprintf("只调用本机 project_comfy_models，参数 category=%q, offset=%d, limit=%d。返回模型名称和下一页位置，不发送微信。", in.Category, in.Offset, in.Limit)
	case "author":
		if strings.TrimSpace(in.Goal) == "" || len(in.Goal) > 4000 {
			return "", toolInputError("station_comfy_goal_required")
		}
		instruction = "先用 project_comfy_workflows、project_comfy_models 和 project_comfy_files 查看现有能力；在工作目录编写 graph JSON，用 project_comfy_register 登记，再查询 workflows 确认。需求：" + in.Goal
	case "run":
		if in.WorkflowID == "" || len(in.WorkflowID) > 96 || !validID(in.WorkflowID) {
			return "", toolInputError("station_comfy_workflow_required")
		}
		parameters, _ := json.Marshal(in.Inputs)
		instruction = "调用本机 project_comfy_run，workflow_id=" + in.WorkflowID + "，inputs=" + string(parameters) + "。返回实际 job_id 和状态；若已完成也返回 artifact_id。不发送到微信。"
	case "upload":
		if (in.SourceMessageID == "") == (in.SourcePath == "") {
			return "", toolInputError("station_comfy_upload_requires_one_source")
		}
		if in.FishHome && in.SourceMessageID != "" {
			return "", toolInputError("station_comfy_upload_scope_invalid")
		}
		var data []byte
		var mime string
		var err error
		if in.SourceMessageID != "" {
			if len(in.SourceMessageID) > 8192 {
				return "", toolInputError("station_comfy_image_source_unavailable")
			}
			message, err := p.manager.native.Message(ctx, in.SourceMessageID)
			if err != nil || message.AccountID != p.trace.Account || message.ThreadID != p.trace.Thread || message.Direction != "incoming" || (message.Type != 3 && message.Type != 43) {
				return "", toolInputError("station_comfy_image_source_unavailable")
			}
			reader, ok := p.manager.native.(incomingReader)
			if !ok {
				return "", toolInputError("station_comfy_image_source_unavailable")
			}
			data, mime, err = reader.IncomingMedia(ctx, message.ID, 128<<20)
			if err != nil {
				return "", err
			}
		} else {
			cfg, err := p.scopedHostConfig(raw)
			if err != nil || len(cfg.Roots) == 0 {
				return "", toolInputError("station_comfy_workspace_unavailable")
			}
			root := cfg.Roots[0]
			rel, err := filepath.Rel(root, in.SourcePath)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
				return "", toolInputError("station_comfy_image_source_unavailable")
			}
			base, err := os.OpenRoot(root)
			if err != nil {
				return "", err
			}
			defer base.Close()
			file, err := base.Open(rel)
			if err != nil {
				return "", err
			}
			defer file.Close()
			data, err = io.ReadAll(io.LimitReader(file, (128<<20)+1))
			if err != nil || len(data) > 128<<20 {
				return "", toolInputError("station_comfy_image_source_unavailable")
			}
			mime = http.DetectContentType(data)
		}
		mediaKind := "image"
		if len(data) >= 12 && string(data[4:8]) == "ftyp" {
			mediaKind = "video"
		}
		if _, err = mcptools.ValidateMediaType(mediaKind, mime, data); err != nil {
			return "", err
		}
		digest := sha256.Sum256(data)
		session, err := p.stationToolLogin(ctx)
		if err != nil {
			return "", err
		}
		result, err := p.stationToolRequest(ctx, session, http.MethodPost, "/api/local/project-tools/comfyui/input", map[string]string{"data_base64": base64.StdEncoding.EncodeToString(data), "sha256": hex.EncodeToString(digest[:])})
		return string(result), err
	case "job":
		if in.JobID == "" || len(in.JobID) > 128 || !validID(in.JobID) {
			return "", toolInputError("station_comfy_job_required")
		}
		instruction = "只调用本机 project_comfy_job 查询 job_id=" + in.JobID + " 的 status，返回实际状态和已登记的 artifact_id。不发送到微信。"
	case "attach":
		if !strings.HasPrefix(in.ArtifactID, "work-") || len(in.ArtifactID) != len("work-")+64 || !validID(in.ArtifactID) {
			return "", toolInputError("station_comfy_artifact_required")
		}
		session, err := p.stationToolLogin(ctx)
		if err != nil {
			return "", err
		}
		return p.attachStationComfyArtifact(ctx, session, in.ArtifactID)
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

// Station owns generated artifacts while the existing WeChat worker owns its
// native upload and delivery ledger. Import the verified bytes through the
// owner's artifact API, then use the normal WeChat staging path.
func (p *permittedTools) attachStationComfyArtifact(ctx context.Context, session stationToolSession, id string) (string, error) {
	data, err := p.stationToolRequest(ctx, session, http.MethodGet, "/api/local/artifacts/"+id, nil)
	if err != nil {
		return "", err
	}
	var artifact struct {
		ID       string `json:"artifact_id"`
		Filename string `json:"filename"`
		SHA256   string `json:"sha256"`
		Bytes    int64  `json:"bytes"`
		Status   string `json:"status"`
	}
	if json.Unmarshal(data, &artifact) != nil || artifact.ID != id || artifact.Status != "registered" || !safeMediaFilename(artifact.Filename) {
		return "", errors.New("station_comfy_artifact_invalid")
	}
	var kind, mimeType string
	switch strings.ToLower(filepath.Ext(artifact.Filename)) {
	case ".png":
		kind, mimeType = "image", "image/png"
	case ".jpg", ".jpeg":
		kind, mimeType = "image", "image/jpeg"
	case ".webp":
		kind, mimeType = "image", "image/webp"
	case ".mp4":
		kind, mimeType = "video", "video/mp4"
	default:
		return "", toolInputError("station_comfy_artifact_media_unsupported")
	}
	limit := toolsLimit(kind)
	if artifact.Bytes < 1 || artifact.Bytes > limit || len(artifact.SHA256) != 64 {
		return "", errors.New("station_comfy_artifact_size_invalid")
	}
	want, err := hex.DecodeString(artifact.SHA256)
	if err != nil || len(want) != sha256.Size {
		return "", errors.New("station_comfy_artifact_digest_invalid")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, session.BaseURL+"/api/local/artifacts/"+id+"/download", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Origin", session.BaseURL)
	req.Header.Set("Cookie", session.Cookie)
	resp, err := (&http.Client{Timeout: 3 * time.Minute}).Do(req)
	if err != nil {
		return "", errors.New("station_comfy_artifact_download_unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("station_comfy_artifact_download_failed_%d", resp.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(content)) != artifact.Bytes {
		return "", errors.New("station_comfy_artifact_download_incomplete")
	}
	digest := sha256.Sum256(content)
	if !bytes.Equal(digest[:], want) {
		return "", errors.New("station_comfy_artifact_digest_mismatch")
	}
	return p.stageOutputWithAsset(ctx, plugins.Output{Text: "工作站成品已准备为微信附件，尚未发送。", Media: []plugins.MediaResult{{Kind: kind, Data: content, MIMEType: mimeType, Filename: artifact.Filename}}}, &assetOriginal{Data: content, MIME: mimeType, Filename: artifact.Filename})
}

func (p *permittedTools) callStationTool(ctx context.Context, action string, raw json.RawMessage) (string, error) {
	if !p.privileged() && !p.workspaceExecutionAllowed() {
		return "", toolInputError("station_tool_not_authorized")
	}
	var in struct {
		Action string `json:"action"`
		ToolID string `json:"tool_id"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", toolInputError("station_tool_arguments_invalid")
	}
	if action == "manage" {
		action = in.Action
	}
	if action != "status" && action != "enable" && action != "start" && action != "stop" && action != "restart" && action != "ensure_ready" && action != "repair" {
		return "", toolInputError("station_tool_action_invalid")
	}
	if action != "status" {
		if _, err := stationToolIDs(in.ToolID); err != nil {
			return "", err
		}
	}
	if in.ToolID == "music-bridge" {
		return p.manageMusicBridge(ctx, action)
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
			out, _ := json.Marshal(map[string]any{"tools": compactStationToolStatus(view.Tools), "music_bridge": p.musicBridgeStatus(ctx), "detail": "station_tool_manage action=status tool_id=<id>"})
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
	if action == "enable" && (!current.Installed || !current.ExternalPackage && !p.privileged()) {
		return "", errors.New("station_tool_owner_install_required")
	}
	if action == "start" || action == "restart" {
		if !current.Enabled {
			return "", errors.New("station_tool_disabled")
		}
	}
	path := "/api/local/project-tools/" + url.PathEscape(id)
	response, err := p.stationToolRequest(ctx, session, http.MethodPut, path, map[string]any{"action": action, "revision": current.Revision, "idle_only": action == "repair" || !p.privileged()})
	if err != nil {
		return "", err
	}
	return string(response), nil
}
