package companion

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"wechat-bot/internal/mcptools"
)

func TestToolRecoveryDistinguishesValidationFromUncertainSubmission(t *testing.T) {
	runner := &AgentRunner{}
	for _, item := range []struct {
		tool, result string
		err          error
		want         toolExecution
	}{
		{"agent_task", "", mcptools.ArgumentError("$.source_message_ids", "invalid"), toolExecution{Status: "failed", ErrorCode: "tool_arguments_invalid", Recovery: "correct_arguments"}},
		{"wechat_send_media", `{"state":"unknown","operation_id":"first"}`, nil, toolExecution{Status: "succeeded", OperationState: "unknown", Recovery: "check_original_operation"}},
		{"music_local_start", `{"state":"queued","job_id":"music-job"}`, nil, toolExecution{Status: "succeeded", OperationState: "queued", Recovery: "continue_original_operation"}},
		{"web_fetch", `{"state":"accepted"}`, nil, toolExecution{Status: "succeeded"}},
	} {
		body := runner.toolFeedback(context.Background(), item.tool, item.result, time.Now(), item.err, false)
		var packet struct {
			Execution toolExecution `json:"execution"`
		}
		if err := json.Unmarshal([]byte(body), &packet); err != nil {
			t.Fatal(err)
		}
		got := packet.Execution
		if got.Status != item.want.Status || got.ErrorCode != item.want.ErrorCode || got.OperationState != item.want.OperationState || got.Recovery != item.want.Recovery {
			t.Fatalf("%s: got %+v, want %+v", item.tool, got, item.want)
		}
	}
}
