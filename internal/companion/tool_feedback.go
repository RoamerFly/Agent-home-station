package companion

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"wechat-bot/internal/mcptools"
)

type toolExecution struct {
	Status         string `json:"status"`
	DurationMS     int64  `json:"duration_ms"`
	Error          string `json:"error,omitempty"`
	ErrorCode      string `json:"error_code,omitempty"`
	Recovery       string `json:"recovery,omitempty"`
	NextTool       string `json:"next_tool,omitempty"`
	OperationState string `json:"operation_state,omitempty"`
	ResultID       string `json:"result_id,omitempty"`
}

func (r *AgentRunner) toolFeedback(ctx context.Context, name, result string, started time.Time, callErr error, blocked bool) string {
	execution := toolExecution{Status: "succeeded", DurationMS: time.Since(started).Milliseconds()}
	if callErr != nil {
		execution.Status = "failed"
		execution.Error = boundedText(result, 800)
		execution.ErrorCode, execution.Recovery, execution.NextTool = toolRecovery(name, callErr)
	} else if !blocked {
		execution.OperationState, execution.Recovery = reportedOperation(name, result)
	}
	if blocked {
		execution.Status = "blocked"
	}
	encodedResult, _ := json.Marshal(result)
	if len(result) > 6000 || len(encodedResult) > 6400 {
		if keeper, ok := r.Tools.(interface {
			RetainToolOutput(context.Context, string, string) (string, error)
		}); ok {
			ref, err := keeper.RetainToolOutput(ctx, name, result)
			if err == nil {
				execution.ResultID = ref
			}
		}
	}
	result = boundedText(result, 6000)
	pack := func() []byte {
		b, _ := json.Marshal(map[string]any{"tool": name, "result": result, "source": "untrusted_tool_result", "execution": execution})
		return b
	}
	payload := pack()
	for len(payload) > 8000 {
		result = boundedText(result, len(result)*3/4)
		payload = pack()
	}
	return string(payload)
}

// A successful RPC can return a queued, rejected or indeterminate operation.
// Only read this envelope from host-owned tools with those semantics.
func reportedOperation(name, result string) (state, recovery string) {
	switch name {
	case "agent_reminder", "agent_task", "music_local_start", "music_rvc_start", "music_local_status", "music_local_resume",
		"wechat_send_text", "wechat_send_media", "wechat_forward_file", "wechat_operation_status", "wechat_delivery_status":
	default:
		return "", ""
	}
	var out struct {
		State      string `json:"state"`
		NotStarted bool   `json:"not_started"`
	}
	if json.Unmarshal([]byte(result), &out) != nil {
		return "", ""
	}
	switch out.State {
	case "queued", "running", "waiting", "waiting_delivery", "pending":
		return out.State, "continue_original_operation"
	case "unknown", "outcome_unknown", "needs_review", "failed", "paused":
		if out.NotStarted {
			return out.State, "restore_dependency_before_submit"
		}
		return out.State, "check_original_operation"
	case "rejected":
		return out.State, "correct_arguments"
	case "accepted", "completed", "scheduled", "cancelled", "stopped":
		return out.State, ""
	default:
		return "", ""
	}
}

func toolRecovery(name string, err error) (code, disposition, nextTool string) {
	var safe interface{ SafeToolFailure() (string, string) }
	var recovery interface{ ToolRecovery() (string, string) }
	var web *mcptools.WebError
	code = "tool_failed"
	switch {
	case errors.Is(err, context.Canceled):
		return "request_cancelled", "stop", ""
	case errors.As(err, &safe):
		code, _ = safe.SafeToolFailure()
	case errors.As(err, &web):
		code = web.Code
	case errors.Is(err, context.DeadlineExceeded):
		code = "tool_timeout"
	}
	if errors.As(err, &recovery) {
		disposition, nextTool = recovery.ToolRecovery()
		return code, disposition, nextTool
	}
	if code == "tool_arguments_invalid" {
		return code, "correct_arguments", ""
	}
	if isReadOnlyRecoveryTool(name) {
		return code, "choose_alternate_read", ""
	}
	return code, "check_original_operation", ""
}
