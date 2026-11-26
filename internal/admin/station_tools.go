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
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wechat-bot/internal/characterrefs"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
	"wechat-bot/internal/pluginhost"
	"wechat-bot/internal/plugins"
)

// stationToolView is deliberately a projection of the Station public tool
// view. It keeps paths, tokens and implementation details out of the Agent
// conversation while allowing future tools to appear without a Go enum.
type stationToolView struct {
	ExecutionHost    string                       `json:"execution_host,omitempty"`
	DirectoryPackage bool                         `json:"directory_package"`
	Backends         []pluginhost.ToolBackendView `json:"backends,omitempty"`
	ExternalPackage  bool                         `json:"external_package,omitempty"`
	ID               string                       `json:"id"`
	Name             string                       `json:"name"`
	Installed        bool                         `json:"installed"`
	Enabled          bool                         `json:"enabled"`
	Busy             bool                         `json:"busy"`
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
			"execution_host": "station", "id": tool.ID, "name": tool.Name, "installed": tool.Installed,
			"enabled": tool.Enabled, "state": tool.State, "busy": tool.Busy,
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

// stationComfyFailure contains only locally chosen diagnostics. Station and
// ComfyUI response bodies can include paths, prompts and provider errors, so
// they must never be passed through to the WeChat tool transcript.
type stationComfyFailure struct {
	Code       string `json:"code"`
	Action     string `json:"action"`
	Field      string `json:"field,omitempty"`
	NotStarted bool   `json:"not_started"`
	NextAction string `json:"next_action"`
	RequestID  string `json:"request_id,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

func (e *stationComfyFailure) Error() string { return e.Code }

func (e *stationComfyFailure) SafeToolFailure() (string, string) {
	message, _ := json.Marshal(e)
	return e.Code, string(message)
}

func (e *stationComfyFailure) ToolRecovery() (string, string) {
	if e.NotStarted && e.Field != "" {
		return "correct_arguments", ""
	}
	if e.NotStarted {
		return "restore_dependency_before_submit", ""
	}
	return "check_original_operation", ""
}

func stationComfyRequestInfo(path string, body any) (action, requestID string, ok bool) {
	switch {
	case path == "/api/local/project-tools/comfyui/action":
		action = "action"
		if values, valid := body.(map[string]any); valid {
			if value, valid := values["action"].(string); valid && (value == "run" || value == "job" || value == "workflows") {
				action = value
			}
			if value, valid := values["request_id"].(string); valid {
				requestID = value
			}
		}
	case path == "/api/local/project-tools/comfyui/input":
		action = "upload"
	case path == "/api/local/chat":
		action = "legacy_chat"
		if values, valid := body.(map[string]string); valid {
			requestID = values["request_id"]
		}
	case strings.HasPrefix(path, "/api/local/artifacts/"):
		action = "attach"
	default:
		return "", "", false
	}
	if len(requestID) > 128 || requestID != "" && !validID(requestID) {
		requestID = ""
	}
	return action, requestID, true
}

func stationComfySafeField(raw string) string {
	if len(raw) == 0 || len(raw) > 80 {
		return ""
	}
	field := strings.TrimPrefix(raw, "inputs.")
	if len(field) == 0 || len(field) > 64 {
		return ""
	}
	for i := 0; i < len(field); i++ {
		c := field[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9' || i > 0 && c == '_' {
			continue
		}
		return ""
	}
	if strings.HasPrefix(raw, "inputs.") {
		return raw
	}
	if field == "workflow_id" || field == "job_id" || field == "request_id" || field == "action" {
		return field
	}
	return "inputs." + field
}

func stationComfyHTTPFailure(status int, data []byte, action, requestID string) *stationComfyFailure {
	var upstream struct {
		Error string `json:"error"`
		Code  string `json:"code"`
		Field string `json:"field"`
	}
	if json.Unmarshal(data, &upstream) != nil {
		upstream.Error = strings.TrimSpace(string(data))
	}
	e := &stationComfyFailure{Code: "station_comfy_request_failed", Action: action, RequestID: requestID, HTTPStatus: status,
		NextAction: "检查工作站 ComfyUI 工具状态；若是生成请求，先查原 request_id 和关联作业，再决定是否继续。"}
	if status == http.StatusBadRequest || status == http.StatusUnprocessableEntity {
		e.Code, e.NotStarted = "station_comfy_arguments_invalid", true
		e.Field = stationComfySafeField(upstream.Field)
		e.NextAction = "修正指定字段，确认原请求没有创建作业后再提交。"
		switch {
		case upstream.Code == "missing_input" || upstream.Code == "comfy_input_missing" || upstream.Code == "missing_required_input" || strings.Contains(upstream.Error, "missing required input"):
			e.Code = "station_comfy_input_missing"
			if e.Field == "" {
				e.Field = "inputs"
			}
		case upstream.Code == "invalid_input" || upstream.Code == "comfy_input_invalid" || upstream.Code == "workflow_input_invalid":
			e.Code = "station_comfy_input_invalid"
			if e.Field == "" {
				e.Field = "inputs"
			}
		case strings.Contains(upstream.Error, "请指定 ComfyUI 工作流") || upstream.Code == "workflow_not_found":
			e.Code, e.Field = "station_comfy_workflow_invalid", "workflow_id"
			e.NextAction = "先读取工作流列表，再使用实际存在的 workflow_id。"
		case strings.Contains(upstream.Error, "请求编号无效"):
			e.Code, e.Field = "station_comfy_request_id_invalid", "request_id"
		}
		return e
	}
	if status == http.StatusConflict {
		switch {
		case strings.Contains(upstream.Error, "同一 ComfyUI 请求编号的参数已变化"):
			e.Code = "station_comfy_request_id_conflict"
		case strings.Contains(upstream.Error, "作业已执行但结果未能保存") || strings.Contains(upstream.Error, "原 ComfyUI 请求结果不可用"):
			e.Code = "station_comfy_run_outcome_unknown"
		case strings.Contains(upstream.Error, "当前工作站会话正在处理"):
			e.Code, e.NotStarted = "station_comfy_busy", true
		case strings.Contains(upstream.Error, "原 ComfyUI 请求来源不可用"):
			e.Code, e.NotStarted = "station_comfy_source_unavailable", true
		case strings.Contains(upstream.Error, "ComfyUI 工具执行未完成") || upstream.Code == "workflow_not_queued":
			e.Code = "station_comfy_run_unfinished"
		default:
			e.Code = "station_comfy_conflict"
		}
	}
	if status >= 500 {
		e.Code = "station_comfy_service_unavailable"
		if strings.Contains(upstream.Error, "工作站正在停止") || strings.Contains(upstream.Error, "请求未能保存") || strings.Contains(upstream.Error, "工具检查点不可用") {
			e.NotStarted = true
		}
	}
	if action == "run" && !e.NotStarted {
		e.NextAction = "按原 request_id 查询工作站请求和关联 job_id；结果未明前不要重新提交生成。"
	}
	return e
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
	comfyAction, comfyRequestID, comfyRequest := stationComfyRequestInfo(path, body)
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
	if (path == "/api/local/chat" || path == "/api/local/project-tools/comfyui/action") && method == http.MethodPost {
		timeout = 3 * time.Minute
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		if comfyRequest {
			failure := &stationComfyFailure{Code: "station_comfy_connection_unavailable", Action: comfyAction, RequestID: comfyRequestID,
				NextAction: "检查本机工作站和 ComfyUI 状态后，沿原请求继续。"}
			if comfyAction == "run" {
				failure.Code = "station_comfy_run_outcome_unknown"
				failure.NextAction = "按原 request_id 查询工作站请求和关联 job_id；结果未明前不要重新提交生成。"
			}
			return nil, failure
		}
		return nil, errors.New("station_tool_request_unavailable")
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if comfyRequest {
			return nil, stationComfyHTTPFailure(resp.StatusCode, data, comfyAction, comfyRequestID)
		}
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
		JobAction       string                     `json:"job_action"`
		ArtifactID      string                     `json:"artifact_id"`
		Category        string                     `json:"category"`
		Offset          int                        `json:"offset"`
		Limit           int                        `json:"limit"`
		Goal            string                     `json:"goal"`
		SourceMessageID string                     `json:"source_message_id"`
		SourcePath      string                     `json:"source_path"`
		OwnMini         bool                       `json:"own_mini"`
		FishHome        bool                       `json:"fish_home"`
		DurationSeconds float64                    `json:"duration_seconds"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil {
		return "", toolInputError("station_comfy_arguments_invalid")
	}
	if in.DurationSeconds < 0 {
		return "", toolInputError("station_comfy_duration_invalid")
	}
	var instruction string
	var effectiveFrames int
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
		if p.trace.TaskID != "" {
			if task, ok := p.manager.agent.lookupTask(p.trace.TaskID, p.trace.Account, p.trace.Thread); ok &&
				task.Sender == p.trace.Sender && task.CorrectionRequiredMediaKind == "video" && task.CorrectionReferenceImageID != "" &&
				(in.SourceMessageID != task.CorrectionReferenceImageID || in.SourcePath != "" || in.OwnMini) {
				return "", toolInputError("纠错视频请显式传新上传图片的 source_message_id；不能用 own_mini 或 source_path")
			}
		}
		imageInput := stationComfyImageInput(in.WorkflowID)
		if imageInput == "" && (in.SourceMessageID != "" || in.SourcePath != "" || in.OwnMini || stationComfyExplicitReference(p.trace.Text)) {
			return "", toolInputError("station_comfy_image_workflow_required")
		}
		if in.OwnMini && (in.SourceMessageID != "" || in.SourcePath != "") {
			return "", toolInputError("station_comfy_image_source_ambiguous")
		}
		if in.OwnMini {
			in.SourcePath = filepath.Join(characterrefs.Directory(p.manager.personaDocumentPath()), "大肥鱼-mini可爱版.jpg")
		}
		if in.SourceMessageID == "" && in.SourcePath == "" && imageInput != "" {
			if messageID := stationComfyCurrentImage(p.trace); messageID != "" {
				in.SourceMessageID = messageID
			} else if stationComfyOwnImageRequested(p.trace.Text) {
				in.SourcePath = filepath.Join(characterrefs.Directory(p.manager.personaDocumentPath()), "大肥鱼-mini可爱版.jpg")
			}
		}
		if in.SourceMessageID != "" || in.SourcePath != "" {
			if imageInput == "" || in.Inputs[imageInput] != nil {
				return "", toolInputError("station_comfy_image_input_ambiguous")
			}
			upload, _ := json.Marshal(map[string]any{"action": "upload", "source_message_id": in.SourceMessageID, "source_path": in.SourcePath, "fish_home": in.FishHome})
			result, err := p.callStationComfy(ctx, upload)
			if err != nil {
				return "", err
			}
			var staged struct {
				Filename string `json:"filename"`
			}
			if json.Unmarshal([]byte(result), &staged) != nil || staged.Filename == "" {
				return "", toolInputError("station_comfy_image_stage_failed")
			}
			if in.Inputs == nil {
				in.Inputs = map[string]json.RawMessage{}
			}
			in.Inputs[imageInput], _ = json.Marshal(staged.Filename)
		}
		if strings.HasPrefix(in.WorkflowID, "h3-") {
			// A conversation can request a 15-second finished film made from
			// two shorter shots. Only an explicit per-run duration controls this
			// workflow; the agent may also set inputs.length directly.
			if in.DurationSeconds > 0 {
				frames := int(math.Round(in.DurationSeconds * 24))
				if frames < 5 || frames > 362 {
					return "", toolInputError("station_comfy_duration_outside_h3_range")
				}
				if in.Inputs == nil {
					in.Inputs = map[string]json.RawMessage{}
				}
				if existing, ok := in.Inputs["length"]; ok {
					var specified int
					if json.Unmarshal(existing, &specified) != nil || specified != frames {
						return "", toolInputError("station_comfy_duration_conflicts_with_length")
					}
				}
				in.Inputs["length"], _ = json.Marshal(frames)
				effectiveFrames = frames
			}
		} else if in.DurationSeconds != 0 {
			return "", toolInputError("station_comfy_duration_requires_video_workflow")
		}
		parameters, _ := json.Marshal(in.Inputs)
		instruction = "调用本机 project_comfy_run，workflow_id=" + in.WorkflowID + "，inputs=" + string(parameters) + "。返回实际 job_id、状态和有效参数；若已完成也返回 artifact_id。不发送到微信。"
		if effectiveFrames > 0 {
			instruction += fmt.Sprintf(" 目标时长 %.2f 秒，必须保留 inputs.length=%d 帧。", float64(effectiveFrames)/24, effectiveFrames)
		}
	case "upload":
		if in.OwnMini {
			if in.SourceMessageID != "" || in.SourcePath != "" {
				return "", toolInputError("station_comfy_image_source_ambiguous")
			}
			in.SourcePath = filepath.Join(characterrefs.Directory(p.manager.personaDocumentPath()), "大肥鱼-mini可爱版.jpg")
		}
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
			// A verified owner can upload the fish's shared character references
			// from any authorized conversation, without mounting the rest of home.
			root := ""
			if characterrefs.Contains(p.manager.personaDocumentPath(), in.SourcePath) {
				root = characterrefs.Directory(p.manager.personaDocumentPath())
			} else {
				cfg, err := p.scopedHostConfig(raw)
				if err != nil || len(cfg.Roots) == 0 {
					return "", toolInputError("station_comfy_workspace_unavailable")
				}
				root = cfg.Roots[0]
			}
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
		if in.JobAction == "" {
			in.JobAction = "status"
		}
		if in.JobAction != "status" && in.JobAction != "cancel" {
			return "", toolInputError("station_comfy_job_action_invalid")
		}
		instruction = "只调用本机 project_comfy_job，对 job_id=" + in.JobID + " 执行 " + in.JobAction + "，返回实际状态。不发送到微信。"
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
	if in.Action == "run" || in.Action == "job" || in.Action == "workflows" {
		requestID := "wechat-comfy-" + agentID(p.trace.Account, p.trace.Thread, p.trace.TraceID, string(raw))
		if in.Action != "run" && !(in.Action == "job" && in.JobAction == "cancel") {
			// Status and catalog are read-only snapshots. A later poll must
			// observe new state instead of replaying the first result.
			requestID = "wechat-comfy-" + agentID(p.trace.Account, p.trace.Thread, p.trace.TraceID, string(raw), fmt.Sprint(time.Now().UnixNano()))
		}
		request := map[string]any{"request_id": requestID, "action": in.Action}
		if in.Action == "run" {
			request["workflow_id"], request["inputs"] = in.WorkflowID, in.Inputs
		}
		if in.Action == "job" {
			request["job_id"] = in.JobID
			request["job_action"] = in.JobAction
		}
		data, err := p.stationToolRequest(ctx, session, http.MethodPost, "/api/local/project-tools/comfyui/action", request)
		if err != nil {
			// An in-flight request created by an older release already owns this
			// ID in local chat. Re-read that exact request through its old route;
			// do not create a second ComfyUI job under a new ID.
			var conflict *stationComfyFailure
			if (in.Action == "run" || in.JobAction == "cancel") && errors.As(err, &conflict) && conflict.Code == "station_comfy_request_id_conflict" {
				return p.stationComfyLegacyChat(ctx, session, requestID, instruction)
			}
			return "", err
		}
		var reply struct {
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(data, &reply) != nil || !json.Valid(reply.Result) {
			return "", errors.New("station_comfy_result_unavailable")
		}
		return string(reply.Result), nil
	}
	requestID := "wechat-comfy-" + agentID(p.trace.Account, p.trace.Thread, p.trace.TraceID, string(raw))
	return p.stationComfyLegacyChat(ctx, session, requestID, instruction)
}

func (p *permittedTools) stationComfyLegacyChat(ctx context.Context, session stationToolSession, requestID, instruction string) (string, error) {
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

func stationComfyImageInput(workflowID string) string {
	switch workflowID {
	case "qwen21-image-to-image-hq", "qwen21-mask-inpaint-hq":
		return "input_image"
	case "h3-image-to-video", "h3-image-reference-to-video", "h3-universal-single", "h3-universal-multi", "h3-image-to-video-singularity":
		return "input_image_1"
	}
	return ""
}

func stationComfyCurrentImage(in companion.Input) string {
	var selected, occurred string
	captionAt, _ := time.Parse(time.RFC3339Nano, in.MessageTime)
	for _, media := range in.IncomingMedia {
		if media.Kind != "image" || media.Source.MessageID == "" || media.Source.Thread != in.Thread {
			continue
		}
		if !captionAt.IsZero() && media.Source.OccurredAt != "" {
			imageAt, err := time.Parse(time.RFC3339Nano, media.Source.OccurredAt)
			if err != nil || imageAt.After(captionAt) {
				continue
			}
		}
		if selected == "" || media.Source.OccurredAt >= occurred {
			selected, occurred = media.Source.MessageID, media.Source.OccurredAt
		}
	}
	// A photo and its correcting caption can be aggregated into one WeChat
	// input. In that shape the caption is the anchor while the photo lives in
	// UnreadMessages. Only an explicit reference instruction selects it here;
	// upload still verifies the exact native message and its thread.
	if stationComfyExplicitReference(in.Text) {
		for _, event := range in.UnreadMessages {
			if event.Source == nil || event.Source.MessageType != 3 || event.Source.Thread != in.Thread ||
				event.Sender != in.Sender || event.Source.MessageID == "" {
				continue
			}
			if !captionAt.IsZero() {
				imageAt, err := time.Parse(time.RFC3339Nano, event.Source.OccurredAt)
				if err != nil || imageAt.After(captionAt) {
					continue
				}
			}
			if selected == "" || event.Source.OccurredAt >= occurred {
				selected, occurred = event.Source.MessageID, event.Source.OccurredAt
			}
		}
	}
	return selected
}

func stationComfyOwnImageRequested(text string) bool {
	text = strings.ToLower(text)
	for _, phrase := range []string{"你自己", "你的形象", "自己的形象", "大肥鱼形象", "mini可爱版", "mini q版", "mini q 版"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

func stationComfyExplicitReference(text string) bool {
	text = strings.ToLower(text)
	for _, phrase := range []string{"只用这张参考图", "只用这张图", "就用这张参考图", "以这张参考图为准", "只用我刚发的图", "就用我刚发的图"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
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
		Action        string `json:"action"`
		ToolID        string `json:"tool_id"`
		ExecutionHost string `json:"execution_host"`
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
	if in.ExecutionHost != "" && in.ExecutionHost != "auto" && in.ExecutionHost != "station" && in.ExecutionHost != "wechat" {
		return "", toolInputError("station_tool_execution_host_invalid")
	}
	if in.ToolID != "" {
		if _, err := stationToolIDs(in.ToolID); err != nil {
			return "", err
		}
	}
	if action != "status" {
		if _, err := stationToolIDs(in.ToolID); err != nil {
			return "", err
		}
	}
	if in.ToolID == "music-bridge" {
		return p.manageMusicBridge(ctx, action)
	}
	if action == "status" && in.ToolID == "" {
		result := map[string]any{"detail": "station_tool_manage action=status tool_id=<id> execution_host=wechat|station"}
		if in.ExecutionHost != "station" {
			views, err := p.wechatToolList(ctx)
			if err != nil {
				result["wechat_error"] = "wechat_tool_status_unavailable"
			} else {
				result["wechat_tools"] = compactWechatToolStatus(views)
			}
		}
		if in.ExecutionHost != "wechat" {
			session, err := p.stationToolLogin(ctx)
			if err == nil {
				var data []byte
				data, err = p.stationToolRequest(ctx, session, http.MethodGet, "/api/local/project-tools", nil)
				if err == nil {
					var view struct {
						Tools []stationToolView `json:"tools"`
					}
					err = json.Unmarshal(data, &view)
					if err == nil {
						result["station_tools"] = compactStationToolStatus(view.Tools)
						result["music_bridge"] = p.musicBridgeStatus(ctx)
					}
				}
			}
			if err != nil {
				result["station_error"] = "station_tool_status_unavailable"
			}
		}
		out, _ := json.Marshal(result)
		return string(out), nil
	}
	// Singing and conversion use Station's managed backends through the music
	// bridge; old account snapshots with these IDs remain available for
	// explicit inspection, but are not the effective execution host.
	preferStation := in.ExecutionHost == "" || in.ExecutionHost == "auto"
	preferStation = preferStation && (in.ToolID == "rvc" || in.ToolID == "yue")
	if in.ExecutionHost != "station" && !preferStation {
		if in.ExecutionHost == "wechat" {
			return p.manageWechatTool(ctx, action, in.ToolID)
		}
		installed, err := p.wechatToolInstalled(ctx, in.ToolID)
		if err != nil {
			return "", errors.New("wechat_tool_status_unavailable")
		}
		if installed {
			return p.manageWechatTool(ctx, action, in.ToolID)
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
		for _, tool := range view.Tools {
			if tool.ID == in.ToolID {
				tool.ExecutionHost = "station"
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
