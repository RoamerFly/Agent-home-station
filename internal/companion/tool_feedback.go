package companion

import (
	"context"
	"encoding/json"
	"time"
)

type toolExecution struct {
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
	ResultID   string `json:"result_id,omitempty"`
}

func (r *AgentRunner) toolFeedback(ctx context.Context, name, result string, started time.Time, callErr error, blocked bool) string {
	execution := toolExecution{Status: "succeeded", DurationMS: time.Since(started).Milliseconds()}
	if callErr != nil {
		execution.Status = "failed"
		execution.Error = boundedText(result, 800)
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
