package station

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/websession"
)

// comfyDirect executes the already registered workstation tool without
// starting another model conversation for a WeChat tool call. Its source and
// checkpoint are the same durable local records used by the normal chat path.
func (h *localChatService) comfyDirect(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.stopping.Load() {
		websession.Fail(w, http.StatusServiceUnavailable, "工作站正在停止")
		return
	}
	var request struct {
		RequestID  string                     `json:"request_id"`
		Action     string                     `json:"action"`
		WorkflowID string                     `json:"workflow_id"`
		Inputs     map[string]json.RawMessage `json:"inputs"`
		JobID      string                     `json:"job_id"`
		JobAction  string                     `json:"job_action"`
		Title      string                     `json:"title"`
	}
	if !websession.Decode(w, r, &request) {
		return
	}
	if !identity.ValidPathID(request.RequestID) || len(request.RequestID) > 128 {
		websession.Fail(w, http.StatusBadRequest, "ComfyUI 请求编号无效")
		return
	}
	var tool string
	var args json.RawMessage
	switch request.Action {
	case "workflows":
		if request.WorkflowID != "" || request.JobID != "" || request.Title != "" || len(request.Inputs) != 0 {
			websession.Fail(w, http.StatusBadRequest, "工作流列表不需要其他参数")
			return
		}
		tool, args = localComfyListTool, json.RawMessage(`{}`)
	case "run":
		if request.WorkflowID == "" || request.JobID != "" {
			websession.Fail(w, http.StatusBadRequest, "请指定 ComfyUI 工作流")
			return
		}
		tool = localComfyStartTool
		args, _ = json.Marshal(map[string]any{"workflow_id": request.WorkflowID, "inputs": request.Inputs, "title": request.Title})
	case "job":
		if request.JobID == "" || request.WorkflowID != "" || request.Title != "" || len(request.Inputs) != 0 {
			websession.Fail(w, http.StatusBadRequest, "请指定 ComfyUI 作业编号")
			return
		}
		if request.JobAction == "" {
			request.JobAction = "status"
		}
		if request.JobAction != "status" && request.JobAction != "cancel" {
			websession.Fail(w, http.StatusBadRequest, "ComfyUI 作业操作无效")
			return
		}
		tool = localComfyJobTool
		args, _ = json.Marshal(map[string]string{"job_id": request.JobID, "action": request.JobAction})
	default:
		websession.Fail(w, http.StatusBadRequest, "ComfyUI 操作无效")
		return
	}
	if !h.tryForegroundTurn(r.Context()) {
		websession.Fail(w, http.StatusConflict, "当前工作站会话正在处理，请稍后查询原作业")
		return
	}
	defer h.turn.Unlock()
	store, input, err := h.conversation()
	if err != nil {
		websession.Fail(w, http.StatusConflict, "本机身份或记录不可用")
		return
	}
	// Keep only a digest in the local transcript. The actual arguments remain
	// in the tool checkpoint and project job rather than bloating future chats.
	digest := sha256.Sum256(append([]byte(request.Action+":"), args...))
	text := "微信 ComfyUI 工具调用 " + request.Action + " " + hex.EncodeToString(digest[:])
	input.Text, input.OriginalText, input.TraceID = text, &text, request.RequestID
	input.MessageTime = time.Now().UTC().Format(time.RFC3339Nano)
	input.Source = &companion.SourceRef{MessageID: request.RequestID}
	prior, err := store.LocalConversationRequest(r.Context(), input, request.RequestID)
	if err != nil {
		websession.Fail(w, http.StatusConflict, "原 ComfyUI 请求来源不可用")
		return
	}
	if prior != nil {
		if prior.InputText != text {
			// Releases before the direct route used this same stable request ID
			// for a local chat instruction. Surface its existing state instead
			// of starting another model or submitting the same render twice.
			if request.Action == "run" && strings.HasPrefix(prior.InputText, "调用本机 project_comfy_run，") {
				state := "legacy_in_progress"
				if prior.Completed {
					state = "legacy_completed"
				}
				websession.Respond(w, http.StatusOK, map[string]any{"result": map[string]any{"state": state, "messages": prior.Messages}, "reused": true})
				return
			}
			websession.Fail(w, http.StatusConflict, "同一 ComfyUI 请求编号的参数已变化")
			return
		}
		input.MessageTime = prior.OccurredAt
		if prior.Completed {
			if len(prior.Messages) != 1 || !json.Valid([]byte(prior.Messages[0])) {
				websession.Fail(w, http.StatusConflict, "原 ComfyUI 请求结果不可用")
				return
			}
			websession.Respond(w, http.StatusOK, map[string]any{"result": json.RawMessage(prior.Messages[0]), "reused": true})
			return
		}
	}
	if err := store.Observe(input, localConversationConfig(""), time.Now().UTC()); err != nil {
		websession.Fail(w, http.StatusServiceUnavailable, "ComfyUI 请求未能保存")
		return
	}
	run, err := store.OpenLocalToolRun(r.Context(), input, prior == nil)
	if err != nil || run == nil || !run.Tracked {
		websession.Fail(w, http.StatusServiceUnavailable, "ComfyUI 工具检查点不可用")
		return
	}
	input.Source, _ = store.SourceForInput(input)
	tools := h.tools(store, input)
	tools.run = run
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	stop := context.AfterFunc(h.ctx, cancel)
	defer stop()
	result, err := tools.callLocalComfy(ctx, tool, args)
	if err != nil || !json.Valid([]byte(result)) {
		websession.Fail(w, http.StatusConflict, "ComfyUI 工具执行未完成："+comfyDirectReason(err))
		return
	}
	if err := store.AcceptLocalConversation(ctx, input, []string{result}, localConversationConfig(""), time.Now().UTC()); err != nil {
		websession.Fail(w, http.StatusServiceUnavailable, "ComfyUI 作业已执行但结果未能保存，请按原请求编号查询")
		return
	}
	websession.Respond(w, http.StatusOK, map[string]any{"result": json.RawMessage(result)})
}

func comfyDirectReason(err error) string {
	if err == nil {
		return "工具未返回有效结果"
	}
	var safe interface{ SafeToolFailure() (string, string) }
	if errors.As(err, &safe) {
		code, _ := safe.SafeToolFailure()
		if code != "" {
			return code
		}
	}
	// The owner can inspect the workstation log for the underlying exception.
	return "comfy_direct_failed"
}
